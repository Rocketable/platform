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
)

func TestGatherOrderedResults(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return gather([
        lambda: slow(id="a", ms=30),
        lambda: slow(id="b", ms=5),
    ])
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{{
		Name: "slow",
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			ms := int(args["ms"].(float64))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(ms) * time.Millisecond):
				return args["id"].(string), nil
			}
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `["a","b"]` {
		t.Fatalf("Run() = %q, want [\"a\",\"b\"]", got)
	}
}

func TestGatherFailFastCancelsSibling(t *testing.T) {
	var (
		mu        sync.Mutex
		cancelled bool
	)

	hangStarted := make(chan struct{})

	_, err := Run(t.Context(), `def main():
    return gather([
        lambda: boom(),
        lambda: hang(),
    ], concurrency=2)
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{
		{
			Name: "boom",
			Call: func(context.Context, map[string]any) (string, error) {
				<-hangStarted
				return "", errors.New("boom")
			},
		},
		{
			Name: "hang",
			Call: func(ctx context.Context, _ map[string]any) (string, error) {
				close(hangStarted)

				select {
				case <-ctx.Done():
					mu.Lock()
					cancelled = true
					mu.Unlock()

					return "", ctx.Err()
				case <-time.After(2 * time.Second):
					return "late", nil
				}
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Run() error = %v, want boom", err)
	}

	mu.Lock()
	ok := cancelled
	mu.Unlock()

	if !ok {
		t.Fatal("expected hang sibling cancelled")
	}
}

func TestMapConcurrencyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu      sync.Mutex
			current int
			maxSeen int
		)

		got, err := Run(t.Context(), `def main():
    return map([1, 2, 3, 4, 5], lambda n: work(n=n), concurrency=2)
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{{
			Name: "work",
			Call: func(_ context.Context, args map[string]any) (string, error) {
				mu.Lock()

				current++
				maxSeen = max(maxSeen, current)
				mu.Unlock()

				time.Sleep(time.Duration(6-int(args["n"].(float64))) * 10 * time.Millisecond)

				mu.Lock()
				current--
				mu.Unlock()

				return fmt.Sprint(args["n"]), nil
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

func TestToolCallObserverReceivesConcurrencyPath(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{name: "gather", source: "def main():\n    return gather([lambda: echo()])\n", want: "gather"},
		{name: "map", source: "def main():\n    return map([1], lambda n: echo())\n", want: "map"},
		{name: "race", source: "def main():\n    return race([lambda: echo()])\n", want: "race"},
		{name: "race first", source: "def main():\n    return race_first([lambda: echo()])\n", want: "race_first"},
		{name: "nested", source: "def main():\n    return gather([lambda: " + "gather([lambda: echo()])])\n", want: "gather/gather"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := ""

			observer := &toolCallObserverMock{ObserveToolCallFunc: func(_ context.Context, steps []string, _ string, _ map[string]any) {
				path = strings.Join(steps, "/")
			}}

			_, err := Run(t.Context(), test.source, nil, allowAll, nilCall, observer, []HostTool{{
				Name: "echo",
				Call: func(_ context.Context, _ map[string]any) (string, error) {
					return "ok", nil
				},
			}})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if path != test.want {
				t.Fatalf("tool call path = %q, want %q", path, test.want)
			}
		})
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
			got, err := Run(t.Context(), "def main():\n    return "+tc.expression+"\n", nil, allowAll, nilCall, inertToolCallObserver{}, nil)
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
	got, err := Run(t.Context(), `def main():
    return gather([])
`, nil, allowAll, nilCall, inertToolCallObserver{}, nil)
	if err != nil {
		t.Fatalf("empty gather error = %v", err)
	}

	if got != `[]` {
		t.Fatalf("empty gather = %q", got)
	}

	_, err = Run(t.Context(), `def main():
    return race([])
`, nil, allowAll, nilCall, inertToolCallObserver{}, nil)
	if err == nil || !strings.Contains(err.Error(), "race") {
		t.Fatalf("empty race error = %v", err)
	}
}

func TestRaceSuccessWins(t *testing.T) {
	var (
		mu           sync.Mutex
		slowFinished bool
	)

	got, err := Run(t.Context(), `def main():
    return race([
        lambda: slow(),
        lambda: fast(),
    ], concurrency=2)
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{
		{
			Name: "fast",
			Call: func(context.Context, map[string]any) (string, error) {
				return "win", nil
			},
		},
		{
			Name: "slow",
			Call: func(ctx context.Context, _ map[string]any) (string, error) {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(500 * time.Millisecond):
					mu.Lock()
					slowFinished = true
					mu.Unlock()

					return "lose", nil
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

func TestRaceAllBranchesFail(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return race([lambda: 1 // 0, lambda: 1 // 0])
`, nil, allowAll, nilCall, inertToolCallObserver{}, nil)
	if err == nil || !strings.Contains(err.Error(), "floored division by zero") {
		t.Fatalf("race error = %v, want division by zero", err)
	}

	if got != "" {
		t.Fatalf("race = %q, want no result when all branches fail", got)
	}
}

func TestRaceFirstFailureWins(t *testing.T) {
	_, err := Run(t.Context(), `def main():
    return race_first([
        lambda: fail_fast(),
        lambda: slow_ok(),
    ], concurrency=2)
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{
		{
			Name: "fail_fast",
			Call: func(context.Context, map[string]any) (string, error) {
				return "", errors.New("first-done-fail")
			},
		},
		{
			Name: "slow_ok",
			Call: func(ctx context.Context, _ map[string]any) (string, error) {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return "ok", nil
				}
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "first-done-fail") {
		t.Fatalf("race_first error = %v, want first-done-fail", err)
	}
}

func TestNestedGather(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return gather([
        lambda: gather([lambda: echo(v="a"), lambda: echo(v="b")]),
        lambda: echo(v="c"),
    ])
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{{
		Name: "echo",
		Call: func(_ context.Context, args map[string]any) (string, error) {
			return args["v"].(string), nil
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `[["a","b"],"c"]` {
		t.Fatalf("nested gather = %q", got)
	}
}

func TestNestedGatherAtMaxConcurrency(t *testing.T) {
	callables := make([]string, 0, MaxConcurrency)

	callables = append(callables, `lambda: gather([lambda: echo(v="a"), lambda: echo(v="b")], concurrency=2)`)
	for range MaxConcurrency - 1 {
		callables = append(callables, `lambda: echo(v="x")`)
	}

	src := "def main():\n    return gather([\n        " + strings.Join(callables, ",\n        ") + fmt.Sprintf("\n    ], concurrency=%d)\n", MaxConcurrency)

	got, err := Run(t.Context(), src, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{{
		Name: "echo",
		Call: func(_ context.Context, args map[string]any) (string, error) {
			return args["v"].(string), nil
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

	go func() {
		_, err := Run(ctx, `def main():
    return gather([lambda: hang()], concurrency=1)
`, nil, allowAll, nilCall, inertToolCallObserver{}, []HostTool{{
			Name: "hang",
			Call: func(callCtx context.Context, _ map[string]any) (string, error) {
				<-callCtx.Done()
				return "", callCtx.Err()
			},
		}})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want cancel error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
