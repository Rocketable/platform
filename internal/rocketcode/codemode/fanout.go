package codemode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const (
	// DefaultConcurrency is the per-call width when concurrency= is omitted.
	DefaultConcurrency = 16
	// MaxConcurrency is the per-call ceiling and the execute-wide in-flight branch cap.
	MaxConcurrency  = 64
	childStepBudget = 100_000

	localCtx      = "ctx"
	localSlotHold = "slotHold"
)

type branchOutcome struct {
	value starlark.Value
	err   error
}

// slotHold tracks one global in-flight token for a fan-out branch goroutine.
// Nested fan-out temporarily releases the parent hold so children can acquire
// without deadlocking at MaxConcurrency.
type slotHold struct {
	slots *semaphore.Weighted
	held  bool
}

func (h *slotHold) acquire(ctx context.Context) error {
	if h.held {
		return nil
	}

	if err := h.slots.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquire concurrency slot: %w", err)
	}

	h.held = true

	return nil
}

func (h *slotHold) release() {
	if !h.held {
		return
	}

	h.slots.Release(1)
	h.held = false
}

func concurrencyBuiltins() starlark.StringDict {
	slots := semaphore.NewWeighted(MaxConcurrency)

	return starlark.StringDict{
		"gather":     starlark.NewBuiltin("gather", makeGather(slots)),
		"map":        starlark.NewBuiltin("map", makeMap(slots)),
		"race":       starlark.NewBuiltin("race", makeRace(slots, false)),
		"race_first": starlark.NewBuiltin("race_first", makeRace(slots, true)),
	}
}

func makeGather(slots *semaphore.Weighted) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		callables, concurrency, err := unpackCallables(b.Name(), args, kwargs)
		if err != nil {
			return nil, err
		}

		out := runGather(thread, slots, concurrencyOpGather, concurrency, callables, nil)

		return out.value, out.err
	}
}

func makeMap(slots *semaphore.Weighted) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var items starlark.Value

		var (
			fn          starlark.Callable
			concurrency starlark.Value = starlark.MakeInt(DefaultConcurrency)
		)

		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "items", &items, "fn", &fn, "concurrency?", &concurrency); err != nil {
			return nil, fmt.Errorf("%s arguments: %w", b.Name(), err)
		}

		limit, err := resolveConcurrency(concurrency)
		if err != nil {
			return nil, err
		}

		values, err := listOrTuple(items)
		if err != nil {
			return nil, fmt.Errorf("%s items: %w", b.Name(), err)
		}

		fn.Freeze()

		for _, value := range values {
			value.Freeze()
		}

		out := runGather(thread, slots, concurrencyOpMap, limit, values, fn)

		return out.value, out.err
	}
}

func makeRace(slots *semaphore.Weighted, firstDone bool) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		callables, concurrency, err := unpackCallables(b.Name(), args, kwargs)
		if err != nil {
			return nil, err
		}

		out := runRace(thread, slots, concurrency, callables, firstDone)

		return out.value, out.err
	}
}

func unpackCallables(name string, args starlark.Tuple, kwargs []starlark.Tuple) ([]starlark.Value, int, error) {
	var (
		callables   starlark.Value
		concurrency starlark.Value = starlark.MakeInt(DefaultConcurrency)
	)

	if err := starlark.UnpackArgs(name, args, kwargs, "callables", &callables, "concurrency?", &concurrency); err != nil {
		return nil, 0, fmt.Errorf("%s arguments: %w", name, err)
	}

	limit, err := resolveConcurrency(concurrency)
	if err != nil {
		return nil, 0, err
	}

	values, err := listOrTuple(callables)
	if err != nil {
		return nil, 0, fmt.Errorf("%s callables: %w", name, err)
	}

	for _, value := range values {
		if _, ok := value.(starlark.Callable); !ok {
			return nil, 0, fmt.Errorf("%s callables must be callable", name)
		}

		value.Freeze()
	}

	return values, limit, nil
}

func resolveConcurrency(value starlark.Value) (int, error) {
	n, err := starlark.AsInt32(value)
	if err != nil {
		return 0, fmt.Errorf("concurrency must be an integer: %w", err)
	}

	if n <= 0 || n > MaxConcurrency {
		return 0, fmt.Errorf("concurrency must be between 1 and %d", MaxConcurrency)
	}

	return n, nil
}

func listOrTuple(value starlark.Value) ([]starlark.Value, error) {
	switch v := value.(type) {
	case *starlark.List:
		return slices.Collect(v.Elements()), nil
	case starlark.Tuple:
		return slices.Clone(v), nil
	default:
		return nil, errors.New("must be a list or tuple")
	}
}

func threadContext(thread *starlark.Thread) (context.Context, error) {
	ctx, ok := thread.Local(localCtx).(context.Context)
	if !ok {
		return nil, errors.New("codemode context missing")
	}

	return ctx, nil
}

// releaseParentSlot frees the caller's global token for the duration of a nested
// fan-out so children can acquire without deadlocking at MaxConcurrency.
// Restore uses a non-canceled context so cancel mid-nest does not leak the permit.
func releaseParentSlot(parent *starlark.Thread) func() {
	hold, ok := parent.Local(localSlotHold).(*slotHold)
	if !ok {
		return func() {}
	}

	hold.release()

	return func() {
		// Restore admission even if the parent branch ctx was canceled mid-nest.
		_ = hold.acquire(context.Background())
	}
}

const (
	concurrencyOpGather = "gather"
	concurrencyOpMap    = "map"
)

func runGather(parent *starlark.Thread, slots *semaphore.Weighted, op string, concurrency int, values []starlark.Value, mapFn starlark.Callable) branchOutcome {
	if len(values) == 0 {
		return branchOutcome{value: starlark.NewList(nil)}
	}

	parentCtx, err := threadContext(parent)
	if err != nil {
		return branchOutcome{err: err}
	}

	defer releaseParentSlot(parent)()

	results := make([]starlark.Value, len(values))
	group, groupCtx := errgroup.WithContext(parentCtx)
	group.SetLimit(concurrency)

	for i, value := range values {
		group.Go(func() error {
			hold := &slotHold{slots: slots}
			if err := hold.acquire(groupCtx); err != nil {
				return err
			}
			defer hold.release()

			var out branchOutcome
			if mapFn != nil {
				out = callBranch(groupCtx, fmt.Sprintf("map-%d", i), mapFn, starlark.Tuple{value}, hold)
			} else {
				out = callBranch(groupCtx, fmt.Sprintf("gather-%d", i), value, nil, hold)
			}

			if out.err != nil {
				return out.err
			}

			results[i] = out.value

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return branchOutcome{err: fmt.Errorf("%s: %w", op, err)}
	}

	return branchOutcome{value: starlark.NewList(results)}
}

func runRace(parent *starlark.Thread, slots *semaphore.Weighted, concurrency int, callables []starlark.Value, firstDone bool) branchOutcome {
	if len(callables) == 0 {
		return branchOutcome{err: errors.New("race requires at least one callable")}
	}

	parentCtx, err := threadContext(parent)
	if err != nil {
		return branchOutcome{err: err}
	}

	defer releaseParentSlot(parent)()

	raceCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var (
		mu      sync.Mutex
		winner  starlark.Value
		errWin  error
		settled bool
		failed  int
	)

	finish := func(out branchOutcome) {
		mu.Lock()
		defer mu.Unlock()

		if settled {
			return
		}

		if firstDone || out.err == nil {
			settled = true
			winner = out.value
			errWin = out.err

			cancel()

			return
		}

		failed++

		if errWin == nil {
			errWin = out.err
		}

		if failed == len(callables) {
			settled = true

			cancel()
		}
	}

	group, groupCtx := errgroup.WithContext(raceCtx)
	group.SetLimit(concurrency)

	for i, callable := range callables {
		if raceCtx.Err() != nil {
			break
		}

		group.Go(func() error {
			if groupCtx.Err() != nil {
				return nil
			}

			hold := &slotHold{slots: slots}
			if err := hold.acquire(groupCtx); err != nil {
				if groupCtx.Err() != nil {
					return nil
				}

				finish(branchOutcome{err: err})

				return nil
			}
			defer hold.release()

			finish(callBranch(groupCtx, fmt.Sprintf("race-%d", i), callable, nil, hold))

			return nil
		})
	}

	_ = group.Wait()

	mu.Lock()
	defer mu.Unlock()

	if errWin != nil {
		return branchOutcome{err: errWin}
	}

	return branchOutcome{value: winner}
}

func callBranch(ctx context.Context, name string, callable starlark.Value, args starlark.Tuple, hold *slotHold) branchOutcome {
	if err := context.Cause(ctx); err != nil {
		return branchOutcome{err: fmt.Errorf("branch context: %w", err)}
	}

	thread, stop := newCancellableThread(ctx, name, childStepBudget)
	defer stop()

	thread.SetLocal(localSlotHold, hold)

	value, err := starlark.Call(thread, callable, args, nil)
	if err != nil {
		if errCtx := context.Cause(ctx); errCtx != nil {
			return branchOutcome{err: fmt.Errorf("branch context: %w", errCtx)}
		}

		return branchOutcome{err: fmt.Errorf("branch call: %w", err)}
	}

	if err := context.Cause(ctx); err != nil {
		return branchOutcome{err: fmt.Errorf("branch context: %w", err)}
	}

	return branchOutcome{value: value}
}

func newCancellableThread(ctx context.Context, name string, maxSteps uint64) (thread *starlark.Thread, stop func()) {
	thread = &starlark.Thread{
		Name:  name,
		Print: func(*starlark.Thread, string) {},
		OnMaxSteps: func(t *starlark.Thread) {
			t.Cancel("step limit exceeded")
		},
	}
	thread.SetMaxExecutionSteps(maxSteps)
	thread.SetLocal(localCtx, ctx)

	stopCancel := context.AfterFunc(ctx, func() {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = context.Canceled
		}

		thread.Cancel(cause.Error())
	})

	return thread, func() { stopCancel() }
}
