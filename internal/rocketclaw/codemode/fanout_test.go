package codemode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.starlark.net/starlark"
)

func TestGatherOrderedResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got, err := runToString(t.Context(), `def main():
    return gather([
        lambda: gather([lambda: slow(id="a", ms=30), lambda: slow(id="b", ms=5)]),
        lambda: slow(id="c", ms=0),
    ])
`, nil, allowAll, nilCall, []HostTool{{
			Details: NoDetails{},
			Name:    "slow",
			CallValue: func(ctx context.Context, args map[string]any) (starlark.Value, error) {
				ms := int(args["ms"].(float64))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(ms) * time.Millisecond):
					return starlark.String(args["id"].(string)), nil
				}
			},
		}})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if got != `[["a","b"],"c"]` {
			t.Fatalf("Run() = %q, want [[\"a\",\"b\"],\"c\"]", got)
		}
	})
}

func TestTryGatherFailFastCancelsSibling(t *testing.T) {
	for branch, want := range map[string]string{"boom()": "boom: boom", "boom().unwrap()": "try_gather: boom: boom"} {
		t.Run(branch, func(t *testing.T) {
			var (
				mu        sync.Mutex
				errCancelled error
			)

			hangStarted := make(chan struct{})

			got, err := runToString(t.Context(), `def main():
    return try_gather([
        lambda: `+branch+`,
        lambda: hang(),
    ], concurrency=2)
`, nil, allowAll, nilCall, []HostTool{
				{
					Details: NoDetails{},
					Name:    "boom",
					CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
						<-hangStarted
						return nil, errors.New("boom")
					},
				},
				{
					Details: NoDetails{},
					Name:    "hang",
					CallValue: func(ctx context.Context, _ map[string]any) (starlark.Value, error) {
						close(hangStarted)

						select {
						case <-ctx.Done():
							mu.Lock()
							errCancelled = context.Cause(ctx)
							mu.Unlock()

							return nil, ctx.Err()
						case <-time.After(2 * time.Second):
							return starlark.String("late"), nil
						}
					},
				},
			})
			if err != nil || got != want {
				t.Fatalf("Run() = %q, %v, want %q", got, err, want)
			}

			mu.Lock()
			errCause := errCancelled
			mu.Unlock()

			if errCause == nil || errCause.Error() != "cancelled after another branch failed" {
				t.Fatalf("hang sibling's cancellation = %v, want it to read as a cancellation", errCause)
			}
		})
	}
}

func TestTryGatherReturnsTheFailureThatCameFirst(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return try_gather([lambda: fail_late(), lambda: fail_now()])
`, nil, allowAll, nilCall, []HostTool{
		{
			Details: NoDetails{},
			Name:    "fail_late",
			CallValue: func(ctx context.Context, _ map[string]any) (starlark.Value, error) {
				<-ctx.Done()

				return nil, errors.New("late")
			},
		},
		{
			Details: NoDetails{},
			Name:    "fail_now",
			CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
				return nil, errors.New("now")
			},
		},
	})
	if err != nil || got != "fail_now: now" {
		t.Fatalf("Run() = %q, %v, want the second branch's earlier failure", got, err)
	}
}

func TestMapConcurrencyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu      sync.Mutex
			current int
			maxSeen int
		)

		got, err := runToString(t.Context(), `def main():
    return map([1, 2, 3, 4, 5], lambda n: work(n=n), concurrency=2)
`, nil, allowAll, nilCall, []HostTool{{
			Details: NoDetails{},
			Name:    "work",
			CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
				mu.Lock()

				current++
				maxSeen = max(maxSeen, current)
				mu.Unlock()

				time.Sleep(time.Duration(6-int(args["n"].(float64))) * 10 * time.Millisecond)

				mu.Lock()
				current--
				mu.Unlock()

				return starlark.String(fmt.Sprint(args["n"])), nil
			},
		}})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if got != `["1","2","3","4","5"]` {
			t.Fatalf("Run() = %q, want [\"1\",\"2\",\"3\",\"4\",\"5\"]", got)
		}

		if maxSeen != 2 {
			t.Fatalf("max in-flight = %d, want 2", maxSeen)
		}
	})
}

func TestFanoutReusesTupleStorage(t *testing.T) {
	tuple := starlark.Tuple{starlark.String("item")}

	values, err := listOrTuple(tuple)
	if err != nil {
		t.Fatal(err)
	}

	if len(values) != len(tuple) || &values[0] != &tuple[0] {
		t.Fatal("fan-out input does not reuse tuple storage")
	}
}

func TestConcurrencyRejectsInvalidArguments(t *testing.T) {
	for _, tc := range []struct {
		name, expression, want string
	}{
		{"zero concurrency", "gather([], concurrency=0)", "concurrency must be between 1 and 64"},
		{"excess concurrency", "gather([], concurrency=65)", "concurrency must be between 1 and 64"},
		{"missing callables", "gather()", "gather arguments: gather: missing argument for callables"},
		{"invalid callables container", "gather(1)", "gather callables: must be a list or tuple"},
		{"noncallable branch", "gather([1])", "gather callables must be callable"},
		{"noninteger concurrency", "gather([], concurrency=1.5)", "concurrency must be an integer:"},
		{"missing map function", "map([])", "map arguments: map: missing argument for fn"},
		{"invalid map concurrency", "map([], lambda n: n, concurrency=0)", "concurrency must be between 1 and 64"},
		{"invalid map items", "map(1, lambda n: n)", "map items: must be a list or tuple"},
		{"missing race callables", "race()", "race arguments: race: missing argument for callables"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runToString(t.Context(), "def main():\n    return "+tc.expression+"\n", nil, allowAll, nilCall, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}

			if got != "" {
				t.Fatalf("Run() = %q, want no result for invalid arguments", got)
			}
		})
	}
}

func TestEmptyGatherAndRace(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return gather([])
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("empty gather error = %v", err)
	}

	if got != `[]` {
		t.Fatalf("empty gather = %q", got)
	}

	_, err = runToString(t.Context(), `def main():
    return race([])
`, nil, allowAll, nilCall, nil)
	if err == nil || !strings.Contains(err.Error(), "race") {
		t.Fatalf("empty race error = %v", err)
	}
}

func TestRaceSuccessWins(t *testing.T) {
	var (
		mu           sync.Mutex
		slowFinished bool
	)

	got, err := runToString(t.Context(), `def main():
    return race([
        lambda: slow(),
        lambda: fast(),
    ], concurrency=2)
`, nil, allowAll, nilCall, []HostTool{
		{
			Details: NoDetails{},
			Name:    "fast",
			CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
				return starlark.String("win"), nil
			},
		},
		{
			Details: NoDetails{},
			Name:    "slow",
			CallValue: func(ctx context.Context, _ map[string]any) (starlark.Value, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					mu.Lock()
					slowFinished = true
					mu.Unlock()

					return starlark.String("lose"), nil
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != "win" {
		t.Fatalf("race = %q, want win", got)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	finished := slowFinished
	mu.Unlock()

	if finished {
		t.Fatal("slow branch published after race win")
	}
}

func TestRaceFirstFailureWins(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return race_first([
        lambda: fail_fast(),
        lambda: slow_ok(),
    ], concurrency=2)
`, nil, allowAll, nilCall, []HostTool{
		{
			Details: NoDetails{},
			Name:    "fail_fast",
			CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
				return nil, errors.New("first-done-fail")
			},
		},
		{
			Details: NoDetails{},
			Name:    "slow_ok",
			CallValue: func(ctx context.Context, _ map[string]any) (starlark.Value, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return starlark.String("ok"), nil
				}
			},
		},
	})
	if err != nil || got != "fail_fast: first-done-fail" {
		t.Fatalf("race_first = %q, %v, want the first-done-fail failure", got, err)
	}
}

func TestNestedGatherAtMaxConcurrency(t *testing.T) {
	callables := make([]string, 0, MaxConcurrency)

	callables = append(callables, `lambda: gather([lambda: echo(v="a"), lambda: echo(v="b")], concurrency=2)`)
	for range MaxConcurrency - 1 {
		callables = append(callables, `lambda: echo(v="x")`)
	}

	src := "def main():\n    return gather([\n        " + strings.Join(callables, ",\n        ") + fmt.Sprintf("\n    ], concurrency=%d)\n", MaxConcurrency)

	got, err := runToString(t.Context(), src, nil, allowAll, nilCall, []HostTool{{
		Details: NoDetails{},
		Name:    "echo",
		CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
			return starlark.String(args["v"].(string)), nil
		},
	}})
	if err != nil {
		t.Fatalf("nested at max concurrency: %v", err)
	}

	if !strings.Contains(got, `"a"`) || !strings.Contains(got, `"x"`) {
		t.Fatalf("unexpected result %q", got)
	}
}

func TestGatherRootCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	hangStarted := make(chan struct{})

	go func() {
		_, err := runToString(ctx, `def main():
    return gather([lambda: hang()], concurrency=1)
`, nil, allowAll, nilCall, []HostTool{{
			Details: NoDetails{},
			Name:    "hang",
			CallValue: func(callCtx context.Context, _ map[string]any) (starlark.Value, error) {
				close(hangStarted)
				<-callCtx.Done()

				return nil, callCtx.Err()
			},
		}})
		errCh <- err
	}()

	select {
	case <-hangStarted:
	case err := <-errCh:
		t.Fatalf("Run returned before the host tool started: %v", err)
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestConcurrencyResults(t *testing.T) {
	runScriptCases(t, map[string]starlark.Value{"t": starlark.String("x"), "e": NewFailure("e", "missing", nil)}, []scriptCase{
		{expression: `gather([lambda: e(), lambda: t()])`, want: `[{"error":"e: missing","ok":false},"x"]`},
		{expression: `map([1, 2], lambda n: e() if n == 1 else t())`, want: `[{"error":"e: missing","ok":false},"x"]`},
		{expression: `try_gather([lambda: t(), lambda: t()])`, want: `["x","x"]`},
		{expression: `try_gather([lambda: t(), lambda: e()]).error`, want: "e: missing"},
		{expression: `try_map([1, 2, 3], lambda n: e() if n == 2 else t())`, want: "e: missing"},
		{expression: `try_map([], lambda n: n)`, want: "[]"},
		{expression: `race([lambda: e(), lambda: t()])`, want: "x"},
		{expression: `race([lambda: e(), lambda: e()])`, want: "e: missing"},
		{expression: `race([lambda: e(), lambda: 1 // 0, lambda: t()])`, want: "x"},
		{expression: `race([lambda: 1 // 0, lambda: 1 // 0]).error`, want: "race: floored division by zero"},
		{expression: `race_first([lambda: fail("boom")]).error`, want: "race_first: fail: boom"},
		{expression: `gather([lambda: e().unwrap(), lambda: t()])`, want: `[{"error":"gather: e: missing","ok":false},"x"]`},
		{expression: `map([0, 1], lambda n: 1 // n)`, want: `[{"error":"map: floored division by zero","ok":false},1]`},
		{expression: `gather([lambda: fail("boom")])[0].unwrap()`, wantErr: "gather: fail: boom"},
		{expression: `try_gather([lambda: t(), lambda: e().unwrap()]).error`, want: "try_gather: e: missing"},
		{expression: `try_map([1], lambda n: n.upper()).unwrap()`, wantErr: "try_map: int has no .upper field or method"},
	})
}

func TestTryGatherResult(t *testing.T) {
	runScriptCases(t, map[string]starlark.Value{"t": starlark.String("x"), "u": starlark.String("y")}, []scriptCase{
		{expression: `type(try_gather([lambda: t(), lambda: u()]))`, want: "gather_result"},
		{expression: `type(try_map([], lambda n: n))`, want: "gather_result"},
		{expression: `(lambda r: [r.ok, r.error, type(r.unwrap()), type(r.or_else(1))])(try_gather([t]))`, want: `[true,null,"gather_result","gather_result"]`},
		{expression: `(lambda r: [len(r), str(r[1]), [str(v) for v in r[1:]], r.index(r[1]), [str(v) for v in r]])(try_gather([t, u]))`, want: `[2,"y",["y"],1,["x","y"]]`},
		{expression: `[bool(try_gather([t])), bool(try_map([], lambda n: n))]`, want: "[true,false]"},
		{expression: `"y" in try_gather([t, u])`, want: "true"},
		{expression: `try_map([1, 2], lambda n: n) == [1, 2]`, want: "true"},
		{expression: `map(try_map([1, 2], lambda n: n), lambda n: n * 10)`, want: "[10,20]"},
		{expression: `try_gather([t]).append(1)`, wantErr: "append: cannot append to frozen list"},
		{expression: `{try_gather([t]): 1}`, wantErr: "unhashable type: gather_result"},
	})
}
