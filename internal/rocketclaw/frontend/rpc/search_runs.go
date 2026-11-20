package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const searchRunsKept = 8

type searchRun struct {
	tab, id string
	work    sessionSearch
	entered bool
	cancel  context.CancelCauseFunc
	done    chan struct{}
	found   *SearchSessionsResponse
	err     error
}

type searchEndError string

func (e searchEndError) Error() string {
	return "search ended: " + string(e)
}

func (s *Server) runSearch(ctx context.Context, request *SearchSessionsRequest, work sessionSearch) (*SearchSessionsResponse, error) {
	s.runsMu.Lock()
	if s.runsStopped {
		s.runsMu.Unlock()

		return nil, fmt.Errorf("web session search: %w", status.Error(codes.Unavailable, "the server is stopping"))
	}

	runs := s.runs[work.username]
	i := slices.IndexFunc(runs, func(run *searchRun) bool { return run.tab == request.Tab && run.id == request.SearchId })

	var run *searchRun
	if i >= 0 && runs[i].work == work && !request.Fresh {
		run = runs[i]
		run.entered = run.entered || request.Entered
	} else {
		entered := request.Entered

		if i >= 0 {
			cause := searchEndError("replaced")
			if runs[i].work == work {
				cause = "refreshed"

				select {
				case <-runs[i].done:
				default:
					entered = entered || runs[i].entered
				}
			}

			runs[i].cancel(cause)
			runs = slices.Delete(runs, i, i+1)
		}

		if len(runs) == searchRunsKept {
			runs[0].cancel(searchEndError("evicted"))
			runs = slices.Delete(runs, 0, 1)
		}

		scanCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		run = &searchRun{tab: request.Tab, id: request.SearchId, work: work, entered: entered, cancel: cancel, done: make(chan struct{})}

		s.runGroup.Go(func() error {
			found, err := work.search(scanCtx, s)

			s.runsMu.Lock()
			defer s.runsMu.Unlock()
			defer close(run.done)

			run.found, run.err = found, err
			if err != nil || !run.entered || !slices.Contains(s.runs[work.username], run) {
				return nil
			}

			if err := s.sessions.MarkSearchFinished(scanCtx, work.username, run.tab); err != nil {
				slog.Error("mark finished search", "tab", run.tab, "error", err)
			}

			return nil
		})

		s.runs[work.username] = append(runs, run)
	}

	s.runsMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for search: %w", ctx.Err())
	case <-run.done:
	}

	s.runsMu.Lock()
	if kept := slices.DeleteFunc(s.runs[work.username], func(kept *searchRun) bool { return kept == run }); len(kept) > 0 {
		s.runs[work.username] = kept
	} else {
		delete(s.runs, work.username)
	}
	s.runsMu.Unlock()

	return run.found, run.err
}

func (s *Server) StopSearches() {
	s.runsMu.Lock()
	s.runsStopped = true

	for _, runs := range s.runs {
		for _, run := range runs {
			run.cancel(searchEndError("stopped"))
		}
	}

	clear(s.runs)
	s.runsMu.Unlock()

	_ = s.runGroup.Wait()
}
