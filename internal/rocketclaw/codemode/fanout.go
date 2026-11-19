package codemode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const (
	DefaultConcurrency = 16
	MaxConcurrency     = 64
	childStepBudget    = 100_000

	localCtx   = "ctx"
	localSlots = "slots"
	localKey   = "key"
	localSteps = "steps"
)

type callKeyContext struct{}

func CallKey(ctx context.Context) string {
	key, _ := ctx.Value(callKeyContext{}).(string)

	return key
}

func nextKey(thread *starlark.Thread) string {
	steps, _ := thread.Local(localSteps).(int)
	thread.SetLocal(localSteps, steps+1)
	prefix, _ := thread.Local(localKey).(string)

	return prefix + strconv.Itoa(steps)
}

func concurrencyBuiltins() starlark.StringDict {
	slots := semaphore.NewWeighted(MaxConcurrency)

	return starlark.StringDict{
		"gather":     starlark.NewBuiltin("gather", makeGather(slots)),
		"map":        starlark.NewBuiltin("map", makeMap(slots)),
		"race":       starlark.NewBuiltin("race", makeRace(slots)),
		"race_first": starlark.NewBuiltin("race_first", makeRace(slots)),
	}
}

func makeGather(slots *semaphore.Weighted) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		callables, concurrency, err := unpackCallables(b.Name(), args, kwargs)
		if err != nil {
			return nil, err
		}

		return runGather(thread, slots, concurrencyOpGather, concurrency, callables, nil)
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

		return runGather(thread, slots, concurrencyOpMap, limit, values, fn)
	}
}

func makeRace(slots *semaphore.Weighted) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		callables, concurrency, err := unpackCallables(b.Name(), args, kwargs)
		if err != nil {
			return nil, err
		}

		return runRace(thread, slots, b.Name(), concurrency, callables)
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

func releaseParentSlot(parent *starlark.Thread) func() {
	slots, ok := parent.Local(localSlots).(*semaphore.Weighted)
	if !ok {
		return func() {}
	}

	slots.Release(1)

	return func() {
		_ = slots.Acquire(context.Background(), 1)
	}
}

const (
	concurrencyOpGather    = "gather"
	concurrencyOpMap       = "map"
	concurrencyOpRaceFirst = "race_first"
)

func runGather(parent *starlark.Thread, slots *semaphore.Weighted, op string, concurrency int, values []starlark.Value, mapFn starlark.Callable) (starlark.Value, error) {
	if len(values) == 0 {
		return starlark.NewList(nil), nil
	}

	parentCtx := parent.Local(localCtx).(context.Context)

	defer releaseParentSlot(parent)()

	results := make([]starlark.Value, len(values))
	group, groupCtx := errgroup.WithContext(parentCtx)
	group.SetLimit(concurrency)

	branchKey := nextKey(parent) + "/"

	for i, value := range values {
		group.Go(func() error {
			if err := slots.Acquire(groupCtx, 1); err != nil {
				return fmt.Errorf("acquire concurrency slot: %w", err)
			}
			defer slots.Release(1)

			var (
				result starlark.Value
				err    error
			)
			if mapFn != nil {
				result, err = callBranch(groupCtx, fmt.Sprintf("map-%d", i), mapFn, starlark.Tuple{value}, slots, branchKey)
			} else {
				result, err = callBranch(groupCtx, fmt.Sprintf("gather-%d", i), value, nil, slots, branchKey)
			}

			if err != nil {
				return err
			}

			results[i] = result

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	return starlark.NewList(results), nil
}

func runRace(parent *starlark.Thread, slots *semaphore.Weighted, op string, concurrency int, callables []starlark.Value) (starlark.Value, error) {
	if len(callables) == 0 {
		return nil, errors.New("race requires at least one callable")
	}

	parentCtx := parent.Local(localCtx).(context.Context)

	defer releaseParentSlot(parent)()

	raceCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var (
		mu      sync.Mutex
		winner  starlark.Value
		errWin  error
		settled bool
	)

	finish := func(value starlark.Value, err error) {
		mu.Lock()
		defer mu.Unlock()

		if settled {
			return
		}

		if op == concurrencyOpRaceFirst || err == nil {
			settled = true
			winner = value
			errWin = err

			cancel()

			return
		}

		if errWin == nil {
			errWin = err
		}
	}

	group, groupCtx := errgroup.WithContext(raceCtx)
	group.SetLimit(concurrency)

	branchKey := nextKey(parent) + "/"

	for i, callable := range callables {
		if raceCtx.Err() != nil {
			break
		}

		group.Go(func() error {
			if groupCtx.Err() != nil {
				return nil
			}

			if slots.Acquire(groupCtx, 1) != nil {
				return nil
			}
			defer slots.Release(1)

			finish(callBranch(groupCtx, fmt.Sprintf("race-%d", i), callable, nil, slots, branchKey))

			return nil
		})
	}

	_ = group.Wait()

	return winner, errWin
}

func callBranch(ctx context.Context, name string, callable starlark.Value, args starlark.Tuple, slots *semaphore.Weighted, key string) (starlark.Value, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, fmt.Errorf("branch context: %w", err)
	}

	thread, stop := newCancellableThread(ctx, name, childStepBudget)
	defer stop()

	thread.SetLocal(localSlots, slots)
	thread.SetLocal(localKey, key+name+"/")

	value, err := starlark.Call(thread, callable, args, nil)

	if errCtx := context.Cause(ctx); errCtx != nil {
		return nil, fmt.Errorf("branch context: %w", errCtx)
	}

	if err != nil {
		return nil, fmt.Errorf("branch call: %w", err)
	}

	return value, nil
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
		thread.Cancel(context.Cause(ctx).Error())
	})

	return thread, func() { stopCancel() }
}
