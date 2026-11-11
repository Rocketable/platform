package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// searchRunsKept bounds the searches kept per person, running or finished.
// ponytail: the bound counts answers, not bytes; a finished answer, message text included, stays in memory until its
// page comes back for it. A server-wide byte budget that drops the oldest kept answer is the upgrade.
const searchRunsKept = 8

// searchRun is the search of one Search page's tab and search id, running on the server for one person: what it
// scans, whether the person entered it, and its scan's cancel. Once done is closed, found and err hold its answer.
type searchRun struct {
	tab, id string
	work    sessionSearch
	entered bool
	cancel  context.CancelCauseFunc
	done    chan struct{}
	found   any
	err     error
}

// searchEndError is why a search running on the server was cancelled.
type searchEndError string

func (e searchEndError) Error() string {
	return "search ended: " + string(e)
}

// runSearch answers a Search page's search from its run on the server, which goes on past the request. The same query
// for the person's tab and search joins its run, or takes its finished answer, which is then dropped; another query,
// or the same one asked fresh because new sessions arrived, replaces the run. A run marks the tab for the person when it finishes, if they entered its query and it is still
// that search's run.
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

				// A run still going that the person entered hands that on, so its tab is still marked once.
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
		runs = append(runs, run)

		s.runGroup.Go(func() error {
			found, err := work.search(scanCtx, s)

			// Holding runsMu across the mark keeps a run replaced, evicted or stopped meanwhile from marking.
			s.runsMu.Lock()
			defer s.runsMu.Unlock()
			defer close(run.done)

			run.found, run.err = found, err
			if err != nil || !run.entered || !slices.Contains(s.runs[work.username], run) {
				return nil
			}

			// A mark that fails is lost, as a missed notification is; it does not fail the server's stop.
			if err := s.sessions.MarkSearchFinished(scanCtx, work.username, run.tab); err != nil {
				slog.Error("mark finished search", "tab", run.tab, "error", err)
			}

			return nil
		})
	}

	s.runs[work.username] = runs
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

	if run.err != nil {
		return nil, run.err
	}

	return run.found.(*SearchSessionsResponse), nil
}

// StopSearches cancels the searches running on the server, none of which marks, and waits for them. No search runs
// after.
func (s *Server) StopSearches() error {
	s.runsMu.Lock()
	s.runsStopped = true

	for _, runs := range s.runs {
		for _, run := range runs {
			run.cancel(searchEndError("stopped"))
		}
	}

	clear(s.runs)
	s.runsMu.Unlock()

	if err := s.runGroup.Wait(); err != nil {
		return fmt.Errorf("finish searches: %w", err)
	}

	return nil
}
