package codemode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGatherOrderedResults(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return gather([
        lambda: slow(id="a", ms=30),
        lambda: slow(id="b", ms=5),
    ])
`, nil, allowAll, nilCall, []HostTool{{
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
`, nil, allowAll, nilCall, []HostTool{
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
	var (
		mu      sync.Mutex
		current int
		maxSeen int
	)

	_, err := Run(t.Context(), `def main():
    return map([1, 2, 3, 4, 5], lambda n: work(n=n), concurrency=2)
`, nil, allowAll, nilCall, []HostTool{{
		Name: "work",
		Call: func(_ context.Context, _ map[string]any) (string, error) {
			mu.Lock()

			current++
			if current > maxSeen {
				maxSeen = current
			}
			mu.Unlock()

			time.Sleep(40 * time.Millisecond)

			mu.Lock()
			current--
			mu.Unlock()

			return "ok", nil
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if maxSeen > 2 {
		t.Fatalf("max in-flight = %d, want <= 2", maxSeen)
	}
}

func TestConcurrencyBounds(t *testing.T) {
	_, err := Run(t.Context(), `def main():
    return gather([], concurrency=0)
`, nil, allowAll, nilCall, nil)
	if err == nil || !strings.Contains(err.Error(), "concurrency") {
		t.Fatalf("concurrency=0 error = %v", err)
	}

	_, err = Run(t.Context(), `def main():
    return gather([], concurrency=65)
`, nil, allowAll, nilCall, nil)
	if err == nil || !strings.Contains(err.Error(), "concurrency") {
		t.Fatalf("concurrency=65 error = %v", err)
	}
}

func TestEmptyGatherAndRace(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return gather([])
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("empty gather error = %v", err)
	}

	if got != `[]` {
		t.Fatalf("empty gather = %q", got)
	}

	_, err = Run(t.Context(), `def main():
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

	got, err := Run(t.Context(), `def main():
    return race([
        lambda: slow(),
        lambda: fast(),
    ], concurrency=2)
`, nil, allowAll, nilCall, []HostTool{
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

func TestRaceFirstFailureWins(t *testing.T) {
	_, err := Run(t.Context(), `def main():
    return race_first([
        lambda: fail_fast(),
        lambda: slow_ok(),
    ], concurrency=2)
`, nil, allowAll, nilCall, []HostTool{
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
`, nil, allowAll, nilCall, []HostTool{{
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

	got, err := Run(t.Context(), src, nil, allowAll, nilCall, []HostTool{{
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
`, nil, allowAll, nilCall, []HostTool{{
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
