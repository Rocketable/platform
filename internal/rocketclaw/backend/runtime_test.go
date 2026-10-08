package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

func TestRuntimeSubscribeIsLiveAndWaitsForDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := new(Runtime)
		history := protocol.NewOutboundMessage("conversation", "old")
		require.NoError(t, rt.PublishOutbound(t.Context(), history))
		events := rt.Subscribe(t.Context())
		message := protocol.NewOutboundMessage("conversation", "new")
		message.Complete = true
		finished := false

		var group errgroup.Group
		group.Go(func() error {
			err := rt.PublishOutbound(t.Context(), message)

			finished = true

			return err
		})

		for event := range events {
			require.Equal(t, "new", event.Message.Text)
			synctest.Wait()
			require.False(t, finished)

			event.Acknowledgement <- nil

			break
		}

		synctest.Wait()
		require.True(t, finished)
		require.NoError(t, group.Wait())
		require.NoError(t, message.WaitDelivered(t.Context()))
	})
}

func TestRuntimeRecordsExplicitConversationsWithoutResettingSelection(t *testing.T) {
	store := newTestSessionService(t)
	rt := &Runtime{Sessions: store}
	require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: "opaque", Agent: "selected", CreatedBy: "cron"}))
	require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: "opaque", Agent: "new-default"}))
	require.NoError(t, store.UpsertExternalMCPSession("external", &ExternalMCPSessionState{PrivateConversationID: "unrecorded-X", ManagedConversationID: "opaque", Agent: "producer"}))
	conversations, err := rt.ListConversations(t.Context())
	require.NoError(t, err)
	require.Equal(t, []protocol.Conversation{{ID: "opaque", Agent: "selected", CreatedBy: "cron"}}, conversations)
}

func TestRuntimeSummaryBackfillLifecycle(t *testing.T) {
	for _, mode := range []string{"cancel", "startup failure", "decode failure", "database failure"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)

			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()

			require.NoError(t, root.Mkdir("agents", 0o755))
			require.NoError(t, root.WriteFile("agents/main.md", []byte("---\ndescription: Backfill test\nmodel: gpt-5.5\n---\nRespond concisely.\n"), 0o600))
			logFile, err := root.Create("runtime.log")

			require.NoError(t, err)
			defer func() { require.NoError(t, logFile.Close()) }()

			service := newTestSessionServiceAt(t, workspace)
			historyID := "bad:" + workspace
			_, err = service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{"timestamp":false}', $2)`, historyID, time.Now().UTC().Format(time.RFC3339Nano))
			require.NoError(t, err)

			if mode == "database failure" {
				_, err = service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{}' WHERE conversation_id = $1`, historyID)
				require.NoError(t, err)
				_, err = service.db.ExecContext(t.Context(), `ALTER TABLE session_summaries ADD CONSTRAINT reject_backfill CHECK (conversation_id = 'live')`)
				require.NoError(t, err)
			}

			// A chat saved before message search; its backfill waits on the same held locks.
			searchID := "search" + strings.ReplaceAll(workspace, "/", ":")
			_, err = service.db.ExecContext(t.Context(), `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, 'main', '')`, searchID)
			require.NoError(t, err)
			insertSessionEntryWithoutSummary(t, service, searchID, testSessionEntry("saved question", "saved answer"))

			tx, err := service.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			defer func() { _ = tx.Rollback() }()

			require.NoError(t, lockSessionHistory(t.Context(), tx, historyID))
			require.NoError(t, lockSessionHistory(t.Context(), tx, searchID))

			// Another worker in the same database must not count as this backfill.
			unrelatedID := "unrelated:" + workspace
			unrelatedTx, err := service.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			require.NoError(t, lockSessionHistory(t.Context(), unrelatedTx, unrelatedID))

			var unrelated errgroup.Group
			unrelated.Go(func() error {
				if err := lockSessionHistory(t.Context(), service.db, unrelatedID); err != nil {
					return fmt.Errorf("wait for unrelated history lock: %w", err)
				}

				return nil
			})

			defer func() {
				require.NoError(t, unrelatedTx.Rollback())
				require.NoError(t, unrelated.Wait())
			}()

			require.Eventually(t, func() bool {
				var waiting bool

				err := service.db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = 87901 AND objid = (hashtext($1)::bigint & 4294967295)::oid AND objsubid = 2 AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))`, unrelatedID).Scan(&waiting)
				require.NoError(t, err)

				return waiting
			}, 5*time.Second, time.Millisecond)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var backfillPID, searchPID int

			assembler := &frontendAssemblerMock{
				ValidateAssetsFunc: func(*config.Config, string, []string) error {
					// Wait for actual contention of both backfills, rather than assuming they started.
					require.Eventually(t, func() bool {
						for id, pid := range map[string]*int{historyID: &backfillPID, searchID: &searchPID} {
							err := service.db.QueryRowContext(t.Context(), `SELECT COALESCE((SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = 87901 AND objid = (hashtext($1)::bigint & 4294967295)::oid AND objsubid = 2 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())), 0)`, id).Scan(pid)
							require.NoError(t, err)
						}

						return backfillPID != 0 && searchPID != 0
					}, 5*time.Second, time.Millisecond)

					if mode == "startup failure" {
						return errors.New("frontend startup failure")
					}

					return nil
				},
				AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
					if mode == "decode failure" || mode == "database failure" {
						require.NoError(t, tx.Commit())

						wantFailure := "parse session summary history"
						if mode == "database failure" {
							wantFailure = "reject_backfill"
						}

						require.Eventually(t, func() bool {
							data, err := root.ReadFile("runtime.log")
							require.NoError(t, err)

							return strings.Contains(string(data), wantFailure)
						}, 5*time.Second, time.Millisecond)
						// A failed summary backfill leaves the message search backfill running.
						require.Eventually(t, func() bool {
							var marked bool
							require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM message_search_indexed WHERE conversation_id = $1)`, searchID).Scan(&marked))

							return marked
						}, 5*time.Second, time.Millisecond)
					}
					// Both blocked and failed backfill leave ordinary work and list reads available.
					require.NoError(t, rt.RunCtx.Err())
					_, err := rt.Sessions.AppendEntryID(ctx, "live", testSessionEntry("available", "answer"))
					require.NoError(t, err)
					summaries, err := rt.Sessions.ListSessions(ctx, []string{"live", historyID})
					require.NoError(t, err)
					require.Equal(t, []protocol.SessionSummary{{ConversationID: "live", LastMessage: "answer", LastUpdated: time.Unix(1, 0).UTC()}}, summaries)

					cancel()

					return nil, nil, nil, nil
				},
			}

			err = Run(ctx, &config.Config{Workspace: workspace, DatabaseURL: testStoreDSN(workspace)}, filepath.Join(workspace, "rocketclaw.json"), slog.New(slog.NewTextHandler(logFile, nil)), assembler)
			if mode == "startup failure" {
				require.ErrorContains(t, err, "frontend startup failure")
			} else {
				require.NoError(t, err)
			}

			// Run cancels and joins the Go worker before closing its database pool.
			// PostgreSQL processes connection closure separately; observe that exact
			// backend disappearing, while the unrelated waiter remains blocked.
			require.Eventually(t, func() bool {
				var stopped bool

				err := service.db.QueryRowContext(t.Context(), `SELECT NOT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid IN ($1, $2))`, backfillPID, searchPID).Scan(&stopped)
				require.NoError(t, err)

				return stopped
			}, 5*time.Second, time.Millisecond, "the observed backfill backends must terminate after Run returns")
		})
	}
}

func TestRuntimeProducerKeepsDestinationUntilSync(t *testing.T) {
	t.Run("sync does not hold history while waiting for bridge", func(t *testing.T) {
		store := newTestSessionService(t)
		manager, _, _, _ := newCronTestManager(t, store)
		ctx := t.Context()
		bridges := make([]*Bridge, 0, 2)

		for _, id := range []string{t.Name() + "-source", t.Name() + "-destination"} {
			require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "job"}))
			bridge, err := manager.recordedBridge(id)
			require.NoError(t, err)

			bridges = append(bridges, bridge)
		}

		source, destination := bridges[0], bridges[1]
		blocker, err := store.db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = blocker.Rollback() }()

		require.NoError(t, lockSessionHistory(ctx, blocker, destination.config.ConversationID))

		var holder int
		require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder))

		var syncing errgroup.Group
		syncing.Go(func() error { return destination.syncConversation(ctx, source) })

		defer func() {
			_ = blocker.Rollback()

			require.NoError(t, syncing.Wait())
		}()

		require.Eventually(t, func() bool {
			var blocked bool
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked))

			return blocked
		}, 5*time.Second, time.Millisecond)

		// Scheduled claims hold the bridge mutex while waiting for this history lock.
		destination.mu.Lock()
		defer destination.mu.Unlock()

		require.NoError(t, blocker.Commit())

		probe, err := store.db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = probe.Rollback() }()

		require.Eventually(t, func() bool {
			var available bool
			require.NoError(t, probe.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(87901, hashtext($1))`, destination.config.ConversationID).Scan(&available))

			return available
		}, 5*time.Second, time.Millisecond, "sync must release history without acquiring the bridge mutex")
	})

	t.Run("history before delivered owner", func(t *testing.T) {
		store := newTestSessionService(t)
		store.db.SetMaxOpenConns(1)
		manager, cfg, roots, finals := newCronTestManager(t, store)
		ctx := t.Context()
		msg := seedCronRun(t, store, "cron:daily")
		source := NewConversation(config.NewLockedConfig(cfg), finalsPublisher{finals: finals}, &Config{ConversationID: msg.ConversationID, Agent: "job", SessionService: store}, slog.New(slog.DiscardHandler))
		source.threads = manager
		require.NoError(t, source.ScheduleMessage(msg, time.Hour, "discard before sync", false))
		require.NoError(t, source.ResetScheduledMessages(msg))
		require.NoError(t, source.ScheduleMessage(msg, 2*time.Hour, "after sync", true))
		endTestTurn(t, store, msg.ConversationID, "turn-cron", protocol.TerminalComplete)

		rt := &Runtime{Sessions: store}
		ownerID := protocol.SlackThreadConversationID("C1", "1.2")
		sourceEntries, err := store.ObserveEntries(ctx, msg.ConversationID)
		require.NoError(t, err)
		require.Len(t, sourceEntries, 3)

		var expected protocol.ScheduledMessageState
		require.NoError(t, json.Unmarshal(sourceEntries[2].Entry.OutputTrace[0], &expected))

		expected.ConversationID, expected.Agent = ownerID, "selected"
		for _, id := range []string{"web:cron:daily", ownerID} {
			require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "selected"}))
			_, err := store.AppendEntryID(ctx, id, testSessionEntry("existing history", "existing answer"))
			require.NoError(t, err)
			destination, err := manager.recordedBridge(id)
			require.NoError(t, err)
			require.NoError(t, destination.syncConversation(ctx, source))

			scheduled, err := store.ScheduledMessagesForConversation(id)
			require.NoError(t, err)
			require.Empty(t, scheduled, "unbound history copies must not apply effects")
		}

		inbound, through, err := (stateDAO{db: store.db}).producer(ctx, msg.ConversationID)
		require.NoError(t, err)
		require.Empty(t, inbound.SyncDestination, "arbitrary Sync must not choose the owner")
		require.Zero(t, through)

		beforeEntries, err := store.ObserveEntries(ctx, ownerID)
		require.NoError(t, err)
		require.Len(t, beforeEntries, 4)

		require.NoError(t, store.PutScheduledMessage("existing", &protocol.ScheduledMessageState{ConversationID: ownerID, Agent: "selected", Message: "preserve on rollback", DueAt: time.Now().Add(time.Hour)}))
		beforeSchedules, err := store.ScheduledMessagesForConversation(ownerID)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `ALTER TABLE managed_conversations ADD CONSTRAINT reject_effect_progress CHECK (producer_effects_through_id = 0) NOT VALID`)
		require.NoError(t, err)

		outbound := source.newOutboundMessage(msg, "turn-cron", "report", true)
		require.ErrorContains(t, source.postCronRoot(ctx, outbound), "reject_effect_progress")
		readFinal(t, roots)

		inbound, through, err = (stateDAO{db: store.db}).producer(ctx, msg.ConversationID)
		require.NoError(t, err)
		require.Equal(t, ownerID, inbound.SyncDestination, "delivered binding must survive projection failure")
		require.Zero(t, through)

		afterSchedules, err := store.ScheduledMessagesForConversation(ownerID)
		require.NoError(t, err)
		require.Equal(t, beforeSchedules, afterSchedules, "failed cursor write must roll back schedule/reset effects")

		_, err = store.db.ExecContext(ctx, `ALTER TABLE managed_conversations DROP CONSTRAINT reject_effect_progress`)
		require.NoError(t, err)
		require.NoError(t, source.postCronRoot(ctx, outbound))
		readFinal(t, roots)

		thread, recorded, err := store.Thread(ownerID)
		require.NoError(t, err)
		require.True(t, recorded)
		require.Equal(t, "selected", thread.Agent, "delivery replay must preserve the canonical selection")

		afterEntries, err := store.ObserveEntries(ctx, ownerID)
		require.NoError(t, err)
		require.Equal(t, beforeEntries, afterEntries, "effects must apply even with zero inserted history rows")

		inbound, through, err = (stateDAO{db: store.db}).producer(ctx, msg.ConversationID)
		require.NoError(t, err)
		require.Equal(t, ownerID, inbound.SyncDestination)
		require.Equal(t, sourceEntries[2].ID, through)

		scheduled, err := store.ScheduledMessagesForConversation(ownerID)
		require.NoError(t, err)
		require.Len(t, scheduled, 1)

		for _, message := range scheduled {
			require.Equal(t, expected, message, "projection preserves the due time and recurring cadence")
		}
	})

	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionService(t)
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    rocketclaw_current_session_id: allow\n    rocketclaw_get_tags: allow\n    rocketclaw_set_tag: [[red, yellow]]\n---\nPrompt\n")

		var tagOutputs []string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input []struct{ Type, Output string }
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				return
			}

			w.Header().Set("Content-Type", "application/json")

			tagOutputs = nil

			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					tagOutputs = append(tagOutputs, item.Output)
				}
			}

			if len(tagOutputs) == 0 {
				writeRawRunFunctionCall(t, w, "tag", "execute", json.RawMessage(`{"code":"def main():\n    return \"\\n\".join([rocketclaw_set_tag(tag=\"red\"), rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_get_tags(), rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_get_tags(), rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_current_session_id()])\n"}`))
			} else {
				writeRawRunMessage(t, w, "done", "message", "done")
			}
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rt := &Runtime{Sessions: store, Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), Log: slog.New(slog.DiscardHandler)}

		rt.threads = newThreadBridgeManager(rt.Cfg, store, rt.Log, func(cfg Config) directBridge {
			cfg.SessionService = store
			return NewConversation(rt.Cfg, rt, &cfg, rt.Log)
		})

		shutdown := runTestManager(t, rt.threads)
		defer func() { require.NoError(t, shutdown()) }()

		for _, id := range []string{"X", "Y", "Z"} {
			require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
			_, err := store.toggleSessionTag(ctx, id, id+"-tag", []string{id + "-tag"})
			require.NoError(t, err)
			replay, err := replayInputForMessage("user", id+" history")
			require.NoError(t, err)
			_, err = store.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: replay, Agent: id, Model: "work/" + id, ReasoningEffort: new("high")})
			require.NoError(t, err)
		}

		events := rt.Subscribe(ctx)

		var delivered, deliveryOrder []string

		go func() {
			for event := range events {
				delivered = append(delivered, event.Message.ConversationID)
				if event.Message.ConversationID == "Y" && event.Message.SlackReply.MessageTS == "producer" {
					assert.Equal(t, "X", event.Message.SourceConversationID)
					assert.Equal(t, "producer", event.Message.Agent)
					assert.Equal(t, "work/producer", event.Message.Model)
					assert.Equal(t, new("high"), event.Message.ReasoningEffort)
				}

				deliveryOrder = append(deliveryOrder, event.Message.SlackReply.MessageTS)
				event.Acknowledgement <- nil
			}
		}()

		producer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
		producer.ConversationID, producer.SyncDestination = "X", "Y"
		producer.SlackReply = &protocol.SlackReplyTarget{MessageTS: "producer"}
		require.NoError(t, rt.RunTurn(ctx, producer))
		server.Close()
		// The private producer's tag tools act on its human-visible destination, while its session ID stays private.
		require.Equal(t, []string{strings.Join([]string{`{"tags":["Y-tag","red"]}`, `{"tags":["Y-tag","yellow"]}`, `{"tags":["Y-tag","yellow"]}`, `{"tags":["Y-tag"]}`, `{"tags":["Y-tag"]}`, `{"tags":["Y-tag","yellow"]}`, "X"}, "\n")}, tagOutputs)

		source := rt.threads.bridges["X"].(*Bridge)
		source.mu.Lock()
		source.pendingOutput.Agent, source.pendingOutput.Model, source.pendingOutput.ReasoningEffort = "producer", "work/producer", new("high")
		source.mu.Unlock()
		require.NoError(t, startTurnDB(ctx, store.db, "producer-effects", "X", producer))
		require.NoError(t, source.ScheduleMessage(producer, time.Hour, "discard before sync", false))
		require.NoError(t, source.ResetScheduledMessages(producer))
		require.NoError(t, source.ScheduleMessage(producer, time.Hour, "after sync", false))
		endTestTurn(t, store, "X", "producer-effects", protocol.TerminalComplete)
		// A copied row's ID, not timestamp magnitude, determines the last update.
		_, err := store.AppendEntryID(ctx, "X", &rocketcode.SessionEntry{Timestamp: time.Unix(1, 123456789).UTC()})
		require.NoError(t, err)

		scheduled, err := store.ScheduledMessagesForConversation("X")
		require.NoError(t, err)
		require.Empty(t, scheduled)

		require.NoError(t, store.PutScheduledMessage("existing", &protocol.ScheduledMessageState{ConversationID: "Y", Agent: "main", Message: "preserve on rollback", DueAt: time.Now().Add(time.Hour)}))

		waitingFinished := false

		var waiting errgroup.Group
		waiting.Go(func() error {
			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindEnqueue, "", true)
			inbound.ConversationID = "Y"
			inbound.AttachmentPresence = protocol.AttachmentPresenceImages
			inbound.SlackReply = &protocol.SlackReplyTarget{MessageTS: "human"}

			err := rt.RunTurn(ctx, inbound)
			waitingFinished = true

			return err
		})
		synctest.Wait()
		require.Equal(t, []string{"X", "X"}, delivered)

		beforeEntries, err := store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		beforeSummaries, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		beforeSchedules, err := store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		// Reject the summary only after Sync has copied entries and applied schedules.
		_, err = store.db.ExecContext(ctx, `ALTER TABLE session_summaries ADD CONSTRAINT reject_sync_summary CHECK (conversation_id <> 'Y') NOT VALID`)
		require.NoError(t, err)
		err = rt.SyncConversation(ctx, "X", "Y")
		require.ErrorContains(t, err, "write session summary")
		require.ErrorContains(t, err, "reject_sync_summary")
		require.NoError(t, ctx.Err())
		synctest.Wait()
		require.False(t, waitingFinished, "failed Sync must retain the destination reservation")
		require.Equal(t, []string{"X", "X"}, delivered)

		afterEntries, err := store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		require.Equal(t, beforeEntries, afterEntries)

		afterSummaries, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		require.Equal(t, beforeSummaries, afterSummaries)

		afterSchedules, err := store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Equal(t, beforeSchedules, afterSchedules)

		_, through, err := (stateDAO{db: store.db}).producer(ctx, "X")
		require.NoError(t, err)
		require.Zero(t, through, "failed Sync must not advance producer effects")

		_, err = store.db.ExecContext(ctx, `ALTER TABLE session_summaries DROP CONSTRAINT reject_sync_summary`)
		require.NoError(t, err)
		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))

		for id, want := range map[string][]string{"X": {"X-tag"}, "Y": {"Y-tag", "yellow"}} {
			tags, err := sessionTags(ctx, store.db, id)
			require.NoError(t, err)
			require.Equal(t, want, tags)
		}

		require.NoError(t, waiting.Wait())
		synctest.Wait()
		require.Equal(t, []string{"X", "X", "Y", "Y"}, delivered)
		require.Equal(t, []string{"producer", "producer", "producer", "human"}, deliveryOrder)

		summaries, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		require.Equal(t, []protocol.SessionSummary{{ConversationID: "Y", LastMessage: "done", LastUpdated: time.Unix(1, 123456000).UTC()}}, summaries)

		for row, err := range store.SidebarSessions(ctx) {
			require.NoError(t, err)

			if row.Conversation.ID == "Y" {
				require.False(t, row.Running)
				require.False(t, row.Pinned)
				require.Equal(t, []string{"Y-tag", "yellow"}, row.Tags)
			}
		}

		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))

		afterSync, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		require.Equal(t, summaries, afterSync)
		synctest.Wait()
		require.Len(t, delivered, 4)

		entries, err := store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		require.Len(t, entries, 7)

		for _, observed := range entries {
			if observed.Entry.Model == "work/X" {
				require.Equal(t, "X", observed.SourceConversationID)
				require.Equal(t, "X", observed.Entry.Agent)
				require.Equal(t, new("high"), observed.Entry.ReasoningEffort)
			}
		}

		messages, err := replayInputMessages(entries[0].Entry.ReplayInput)
		require.NoError(t, err)
		require.Equal(t, "Y history", messages[0].text)

		entries, err = store.ObserveEntries(ctx, "X")
		require.NoError(t, err)
		require.Len(t, entries, 6)
		sourceEntries := entries

		scheduled, err = store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Len(t, scheduled, 1)

		for _, message := range scheduled {
			require.Equal(t, "after sync", message.Message)
		}

		// These attachment-fallback turns exercise runtime routing, not provider
		// continuation. Seed the Y-only reply because fallback does not record it.
		reply := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", true)
		reply.ConversationID = "Y"
		reply.AttachmentPresence = protocol.AttachmentPresenceImages
		reply.SlackReply = &protocol.SlackReplyTarget{MessageTS: "reply"}
		require.NoError(t, rt.RunTurn(ctx, reply))

		replay, err := replayInputForMessage("user", "Y-only reply after sync")
		require.NoError(t, err)
		_, err = store.AppendEntryID(ctx, "Y", &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: replay})
		require.NoError(t, err)
		destinationEntries, err := store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		require.Len(t, destinationEntries, 8)

		continuation := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
		continuation.ConversationID, continuation.SyncDestination = "X", "Y"
		continuation.AttachmentPresence = protocol.AttachmentPresenceImages
		continuation.SlackReply = &protocol.SlackReplyTarget{MessageTS: "continuation"}
		require.NoError(t, rt.RunTurn(ctx, continuation))
		synctest.Wait()
		require.Equal(t, []string{"X", "X", "Y", "Y", "Y", "X"}, delivered)

		entries, err = store.ObserveEntries(ctx, "X")
		require.NoError(t, err)
		require.Equal(t, sourceEntries, entries, "Y history and reply must stay off X")
		entries, err = store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		require.Equal(t, destinationEntries, entries, "continuing X must not copy entries without another Sync")

		scheduledAfter, err := store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Equal(t, scheduled, scheduledAfter, "continuing X must not duplicate copied effects")

		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))
		synctest.Wait()
		require.Equal(t, []string{"X", "X", "Y", "Y", "Y", "X", "Y"}, delivered)

		entries, err = store.ObserveEntries(ctx, "X")
		require.NoError(t, err)
		require.Equal(t, sourceEntries, entries, "subsequent Sync must not copy Y history or reply back to X")
		entries, err = store.ObserveEntries(ctx, "Y")
		require.NoError(t, err)
		require.Equal(t, destinationEntries, entries, "subsequent Sync must not duplicate copied entries")

		for id, message := range scheduled {
			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, message.Message, false)
			inbound.ConversationID = "Y"
			request := &bridgeRequest{inbound: inbound, scheduledMessageID: id}
			admitted, err := rt.threads.bridges["Y"].(*Bridge).activateInbound(ctx, request)
			require.NoError(t, err)
			require.True(t, admitted)
			endTestTurn(t, store, "Y", request.turnID, protocol.TerminalComplete)
		}

		require.NoError(t, store.PutScheduledMessage("Z-existing", &protocol.ScheduledMessageState{ConversationID: "Z", Agent: "main", Message: "keep Z", DueAt: time.Now().Add(time.Hour)}))
		beforeZ, err := store.ScheduledMessagesForConversation("Z")
		require.NoError(t, err)

		for _, pair := range [][2]string{{"X", "Y"}, {"X", "Z"}, {"Y", "Z"}} {
			require.NoError(t, rt.SyncConversation(ctx, pair[0], pair[1]))
		}

		scheduled, err = store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Empty(t, scheduled, "repeated Sync must not rearm a consumed one-shot")

		afterZ, err := store.ScheduledMessagesForConversation("Z")
		require.NoError(t, err)
		require.Equal(t, beforeZ, afterZ, "alternate and onward history copies must not replay schedules or resets")

		zEntries, err := store.ObserveEntries(ctx, "Z")
		require.NoError(t, err)
		require.Greater(t, len(zEntries), len(sourceEntries), "both copies still expose history")

		beforeDelete, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		_, err = store.DeleteSession(ctx, "X")
		require.NoError(t, err)
		afterDelete, err := store.ListSessions(ctx, []string{"Y"})
		require.NoError(t, err)
		require.Equal(t, beforeDelete, afterDelete)

		scheduledAfter, err = store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Equal(t, scheduled, scheduledAfter, "subsequent Sync must not duplicate copied effects")
	})
}

func TestRuntimePersistedEnqueueAndProducerArrivalOrder(t *testing.T) {
	for _, tt := range []struct {
		name         string
		producerID   string
		enqueueFirst bool
	}{
		{"same X/enqueue first", "X", true},
		{"same X/producer first", "X", false},
		{"distinct X2/enqueue first", "X2", true},
		{"distinct X2/producer first", "X2", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newTestSessionService(t)

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				rt := &Runtime{Sessions: store, Cfg: new(config.LockedConfig), Log: slog.New(slog.DiscardHandler)}

				rt.threads = newThreadBridgeManager(rt.Cfg, store, rt.Log, func(cfg Config) directBridge {
					cfg.SessionService = store
					return NewConversation(rt.Cfg, rt, &cfg, rt.Log)
				})

				shutdown := runTestManager(t, rt.threads)
				defer func() { require.NoError(t, shutdown()) }()

				target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
				destination := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)

				for _, id := range []string{"X", "X2", destination} {
					require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
				}

				events := rt.Subscribe(ctx)

				var (
					delivered, order []string
					listeners        errgroup.Group
				)
				listeners.Go(func() error {
					for event := range events {
						if event.Message.ConsumedID == "" {
							delivered = append(delivered, event.Message.ConversationID)
							order = append(order, event.Message.SlackReply.MessageTS)
						}

						event.Acknowledgement <- nil
					}

					return nil
				})

				producer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)

				producer.ConversationID, producer.SyncDestination = "X", destination
				producer.AttachmentPresence = protocol.AttachmentPresenceImages
				producer.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "producer"}
				require.NoError(t, rt.RunTurn(ctx, producer))

				var waiting errgroup.Group
				waiting.Go(func() error {
					steer := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "", true)

					steer.ConversationID = destination
					steer.AttachmentPresence = protocol.AttachmentPresenceImages
					steer.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "steer"}

					return rt.RunTurn(ctx, steer)
				})
				synctest.Wait()

				competing := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
				competing.ConversationID, competing.SyncDestination = tt.producerID, destination
				competing.AttachmentPresence = protocol.AttachmentPresenceImages
				competing.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "competing"}

				for _, enqueue := range []bool{tt.enqueueFirst, !tt.enqueueFirst} {
					if enqueue {
						content := protocol.InboundContent{AttachmentPresence: protocol.AttachmentPresenceImages}
						require.NoError(t, rt.threads.StashThreadQueueItem(ctx, target, &protocol.ThreadQueueItem{ID: "enqueue", Kind: protocol.InboundKindEnqueue, Source: protocol.SourceSlack, Content: content, Principal: "original author", StashAt: time.Now(), SlackChannel: target.ChannelID, SlackTS: "enqueue", SlackReply: &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: "enqueue", ThreadTS: target.ThreadID}}))
					} else {
						waiting.Go(func() error {
							if err := rt.RunTurn(ctx, competing); err != nil {
								return err
							}

							return rt.SyncConversation(ctx, tt.producerID, destination)
						})
					}

					synctest.Wait()
				}

				require.Equal(t, []string{"X"}, delivered)

				// The fallback turn has finished, but its real producer reservation
				// still holds Y. Install X's active cancellation state at this boundary.
				turnCtx, cancelTurn := context.WithCancel(ctx)
				defer cancelTurn()

				source := rt.threads.bridges["X"].(*Bridge)
				source.mu.Lock()
				source.activeReply, source.activeTurnCancel = producer, cancelTurn
				source.mu.Unlock()

				if rt.threads.InterruptConversation(destination) != producer {
					t.Error("interrupt Y must return X's active inbound")
				}

				if !errors.Is(turnCtx.Err(), context.Canceled) {
					t.Error("interrupt Y must cancel X's active turn")
				}

				synctest.Wait()
				require.Equal(t, []string{"X"}, delivered, "interrupt must not release Y's waiting work")

				failedSync, cancelSync := context.WithCancel(ctx)
				cancelSync()
				require.ErrorIs(t, rt.SyncConversation(failedSync, "X", destination), context.Canceled)
				synctest.Wait()
				require.Equal(t, []string{"X"}, delivered, "failed Sync must hold both waiting paths")
				require.NoError(t, rt.SyncConversation(ctx, "X", destination))
				require.NoError(t, waiting.Wait())
				synctest.Wait()
				cancel()
				require.NoError(t, listeners.Wait())

				want := []string{"X", destination, destination, tt.producerID, destination, destination}
				wantOrder := []string{"producer", "producer", "steer", "competing", "competing", "enqueue"}

				if tt.enqueueFirst {
					want = []string{"X", destination, destination, destination, tt.producerID, destination}
					wantOrder = []string{"producer", "producer", "steer", "enqueue", "competing", "competing"}
				}

				require.Equal(t, want, delivered, "persisted enqueue and competing producer must share arrival order")
				require.Equal(t, wantOrder, order, "waiting steer and enqueue keep their original outbound targets")
			})
		})
	}
}

func TestRuntimeSteersWaitForTheirTurnDelivery(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.Close())

	// Keep the HTTP listener outside the bubble and close each connection so
	// idle network reads do not prevent the delivery-boundary Wait calls.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")

		_, errWrite := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
		if errWrite != nil {
			t.Error(errWrite)
		}
	}))
	defer server.Close()

	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionServiceAt(t, workspace)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rt := &Runtime{Sessions: store, Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), Log: slog.New(slog.DiscardHandler)}
		atDrain, resume := make(chan struct{}), make(chan struct{})
		drains := 0

		rt.threads = newThreadBridgeManager(rt.Cfg, store, rt.Log, func(cfg Config) directBridge {
			cfg.SessionService = store
			cfg.SteerDrain = rocketcode.SteerDrain{Fn: func(context.Context, rocketcode.TurnPhase) []rocketcode.PromptInput {
				drains++
				if drains == 1 {
					close(atDrain)
					<-resume
				}

				return nil
			}}

			return NewConversation(rt.Cfg, rt, &cfg, rt.Log)
		})

		shutdown := runTestManager(t, rt.threads)
		defer func() { require.NoError(t, shutdown()) }()

		require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "Y", Agent: "main"}))

		events := rt.Subscribe(ctx)
		finals := make(chan protocol.Event, 4)

		var listeners errgroup.Group
		listeners.Go(func() error {
			for event := range events {
				if event.Message.Complete {
					finals <- event
				} else {
					event.Acknowledgement <- nil
				}
			}

			return nil
		})

		var (
			calls    errgroup.Group
			returned [4]bool
		)

		for i, text := range []string{"idle", "active", "late first", "late second"} {
			inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, text, true)
			inbound.ConversationID = "Y"
			inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "root", MessageTS: text}

			calls.Go(func() error {
				err := rt.RunTurn(ctx, inbound)
				returned[i] = true

				return err
			})

			if i == 0 {
				<-atDrain
			}

			synctest.Wait()
			require.Equal(t, [4]bool{}, returned, "acceptance must not complete any call")

			if i == 1 {
				bridge := rt.threads.bridges["Y"].(*Bridge)
				require.Len(t, bridge.steers, 1)
				require.Empty(t, bridge.requestCh, "active steer must join, not queue a turn")
				close(resume)
				synctest.Wait()
				require.Len(t, finals, 1)
				require.Equal(t, 2, drains, "active input must trigger provider continuation in the same turn")
				require.False(t, bridge.inputOpen)
			}
		}

		const answer = "answer"

		for i, text := range []string{"idle", "late first", "late second"} {
			final := <-finals
			require.Equal(t, "Y", final.Message.ConversationID)
			require.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "root", MessageTS: text}, final.Message.SlackReply)
			require.Equal(t, []string{answer + "\n" + answer, answer, answer}[i], final.Message.Text)
			synctest.Wait()
			require.Equal(t, [4]bool{i > 0, i > 0, i > 1, false}, returned)

			final.Acknowledgement <- nil
		}

		require.NoError(t, calls.Wait())
		require.Equal(t, [4]bool{true, true, true, true}, returned)
		require.Empty(t, finals, "active steer must not produce a separate final")
		cancel()
		require.NoError(t, listeners.Wait())
	})
}

func TestBridgeDrainSteersPreservesAcquiredContent(t *testing.T) {
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), inputOpen: true, requestCh: make(chan bridgeRequest, 2), config: Config{SessionService: newTestSessionService(t)}}

	for _, text := range []string{"first", "second"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$docs-helper " + text, TextAttachments: []string{"attachment text"}, Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte(text)}}}, true)
		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "U1"
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	drain := rocketcode.SteerDrain{Fn: bridge.drainSteers}
	inputs := drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 2)

	for i, text := range []string{"first", "second"} {
		require.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "docs-helper", Arguments: text}, inputs[i].DirectSkill)
		require.Contains(t, inputs[i].Text, "attachment text")
		require.Contains(t, inputs[i].Text, text)
		require.Contains(t, inputs[i].Text, "U1")
		require.Equal(t, []rocketcode.Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64," + []string{"Zmlyc3Q=", "c2Vjb25k"}[i]}}, inputs[i].Attachments)
	}

	require.True(t, bridge.inputOpen, "injected input reopens provider work")
	require.Empty(t, drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer))
	require.False(t, bridge.inputOpen)

	late := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, &protocol.InboundContent{Text: "late", Attachments: []protocol.InboundAttachment{{Name: "late.png", MIMEType: "image/png", Data: []byte("late")}}}, true)
	late.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "U2"}
	require.NoError(t, bridge.Submit(t.Context(), late))
	require.Empty(t, drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer))
	require.Same(t, late, (<-bridge.requestCh).inbound, "cutoff steer keeps its full input for the next turn")
}

func TestThreadBridgeManagerWaitingSteerControls(t *testing.T) {
	store := newTestSessionService(t)
	target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: discardPublisher{}, config: Config{ConversationID: conversationID, SessionService: store}, inputOpen: true, requestCh: make(chan bridgeRequest, 2)}
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{conversationID: bridge}}
	active := &turnCompletion{done: make(chan struct{})}
	bridge.activeCompletion = active

	for _, text := range []string{"first", "drop", "last"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, &protocol.InboundContent{Text: text, Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte(text)}}}, true)
		inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: text + " author"}
		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: text}
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	items, err := manager.ThreadQueueItems(target)
	require.NoError(t, err)
	require.Len(t, items, 3)

	for i, text := range []string{"first", "drop", "last"} {
		require.NotEmpty(t, items[i].ID)
		require.Equal(t, protocol.InboundKindSteer, items[i].Kind)
		require.Equal(t, text, items[i].Message)
		require.Equal(t, text+" author", items[i].Principal)
		require.Equal(t, target.ChannelID, items[i].SlackChannel)
		require.Equal(t, text, items[i].SlackTS)
	}

	dropped := bridge.steers[1].completion
	removed, err := manager.DeleteThreadQueueItem(t.Context(), target, items[1].ID)
	require.NoError(t, err)
	require.True(t, removed)
	require.ErrorIs(t, dropped.err, context.Canceled)

	select {
	case <-dropped.done:
	default:
		t.Fatal("dropped request still waiting for completion")
	}

	select {
	case <-active.done:
		t.Fatal("dropping a waiting steer ended the active turn")
	default:
	}

	inputs := bridge.drainSteers(t.Context(), rocketcode.TurnPhaseToolLoop)
	require.Len(t, inputs, 2)

	for i, text := range []string{"first", "last"} {
		require.Contains(t, inputs[i].Text, text+" author")
		require.Equal(t, []rocketcode.Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64," + []string{"Zmlyc3Q=", "bGFzdA=="}[i]}}, inputs[i].Attachments)
	}

	itemsAfter, err := manager.ThreadQueueItems(target)
	require.NoError(t, err)
	require.Empty(t, itemsAfter)

	for _, item := range items {
		removed, err := manager.DeleteThreadQueueItem(t.Context(), target, item.ID)
		require.NoError(t, err)
		require.False(t, removed, "consumed or dropped steer cannot be dropped again")
	}

	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	content := protocol.InboundContent{Text: "$skill stop \"typed args\"  Next", TextAttachments: []string{"acquired text file", "acquired forwarded thread"}, Attachments: []protocol.InboundAttachment{{Name: "original.png", MIMEType: "image/png", Data: []byte("original")}}}
	queued := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindEnqueue, &content, true)
	queued.Metadata[protocol.InboundPrincipalMetadataKey] = "original author"
	queued.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "promoted", RecipientTeamID: "T1", RecipientUserID: "U1"}
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q1", Message: content.Text, Content: content, Source: queued.Source, SlackReply: queued.SlackReply, Principal: "original author", SlackChannel: target.ChannelID, SlackTS: "promoted"}))

	var (
		promotions [2]bool
		group      errgroup.Group
	)
	for i := range promotions {
		group.Go(func() error {
			var err error

			promotions[i], err = manager.PromoteThreadQueueItem(t.Context(), target, "q1")

			return err
		})
	}

	require.NoError(t, group.Wait())
	require.NotEqual(t, promotions[0], promotions[1], "only one competing promotion may claim the enqueue")

	itemsAfter, err = manager.ThreadQueueItems(target)
	require.NoError(t, err)
	require.Len(t, itemsAfter, 1)
	require.Equal(t, protocol.InboundKindSteer, itemsAfter[0].Kind)
	require.Equal(t, "original author", itemsAfter[0].Principal)

	promoted := bridge.steers[bridge.steersRead].inbound
	require.Equal(t, queued.Source, promoted.Source)
	require.Equal(t, queued.Text, promoted.Text)
	require.Equal(t, queued.SlackReply, promoted.SlackReply)
	inputs = bridge.drainSteers(t.Context(), rocketcode.TurnPhaseToolLoop)
	require.Len(t, inputs, 1)
	require.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "stop", Arguments: "\"typed args\"  Next"}, inputs[0].DirectSkill)
	require.Contains(t, inputs[0].Text, "original author")
	require.Contains(t, inputs[0].Text, "acquired text file\n\nacquired forwarded thread")
	require.Equal(t, []rocketcode.Attachment{{MIME: "image/png", Filename: "original.png", URL: "data:image/png;base64,b3JpZ2luYWw="}}, inputs[0].Attachments)
	_, claimed, err := (stateDAO{db: store.db}).claimThreadQueueItem(t.Context(), conversationID, "q1")
	require.NoError(t, err)
	require.False(t, claimed, "normal consumption cannot claim the promoted enqueue")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})
		bridge.log = slog.New(slog.DiscardHandler)
		bridge.config.EnqueueActivation = EnqueueActivation{Fn: func(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
			t.Error("normal queue consumer activated an already promoted enqueue")
			return context.Canceled
		}}
		require.NoError(t, bridge.submitEnqueuedItem(ctx, &protocol.ThreadQueueItem{ID: "q1", Message: "stale queue snapshot"}))

		var group errgroup.Group
		group.Go(func() error {
			bridge.loop(ctx)
			return nil
		})
		synctest.Wait()
		require.Empty(t, bridge.requestCh)
		require.True(t, bridge.inputOpen, "stale queue consumption must not start or end a turn")
		cancel()
		require.NoError(t, group.Wait())
	})

	for _, inputOpen := range []bool{true, false} {
		bridge = &Bridge{log: slog.New(slog.DiscardHandler), bus: discardPublisher{}, config: Config{ConversationID: conversationID, SessionService: store}, inputOpen: inputOpen, requestCh: make(chan bridgeRequest, 1)}

		manager.bridges[conversationID] = bridge
		for _, id := range []string{"before", "attachment", "after"} {
			require.NoError(t, store.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ID: id, ConversationID: conversationID, Source: protocol.SourceWeb, Principal: "goal_continuation", Content: protocol.InboundContent{TextAttachments: []string{"$docs-helper attachment-only"}}}))
		}

		promoted, err := manager.PromoteThreadQueueItem(t.Context(), target, "attachment")
		require.NoError(t, err)
		require.True(t, promoted)

		if inputOpen {
			inputs := bridge.drainSteers(t.Context(), rocketcode.TurnPhaseToolLoop)
			require.Len(t, inputs, 1)
			require.Nil(t, inputs[0].DirectSkill)
			require.Contains(t, inputs[0].Text, "goal_continuation")
			require.Contains(t, inputs[0].Text, "$docs-helper attachment-only")
		} else {
			inbound := (<-bridge.requestCh).inbound
			require.Equal(t, "goal_continuation", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
			require.Nil(t, inboundDirectSkill(inbound))
			require.Empty(t, inbound.Metadata[protocol.InboundRawTextMetadataKey])
			require.Contains(t, inbound.Text, "$docs-helper attachment-only")
		}

		remaining, err := store.ThreadQueueForConversation(conversationID)
		require.NoError(t, err)
		require.Len(t, remaining, 2)
		require.Equal(t, "before", remaining[0].ID)
		require.Equal(t, "after", remaining[1].ID)

		for _, item := range remaining {
			removed, err := manager.DeleteThreadQueueItem(t.Context(), target, item.ID)
			require.NoError(t, err)
			require.True(t, removed)
		}
	}
}

func TestRuntimeRunTurnCancelPublishesEmptyComplete(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.0")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)
	rt := &Runtime{threads: manager, Sessions: store}
	events := rt.Subscribe(t.Context())

	var (
		delivered *protocol.OutboundMessage
		listeners errgroup.Group
	)
	listeners.Go(func() error {
		for event := range events {
			delivered = event.Message
			event.Acknowledgement <- nil

			break
		}

		return nil
	})

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindCancel, "", true)
	inbound.ConversationID = conversationID
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	require.NoError(t, listeners.Wait())
	require.NotNil(t, delivered)
	require.True(t, delivered.Complete)
	require.Empty(t, delivered.Text)
	require.Equal(t, conversationID, delivered.ConversationID)

	done := make(chan struct{})
	close(done)
	bridge.activeCompletion = &turnCompletion{done: done, err: context.Canceled}
	canceled := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindCancel, "", true)
	canceled.ConversationID = conversationID
	require.ErrorIs(t, rt.RunTurn(t.Context(), canceled), context.Canceled)
}

func TestRuntimeRunTurnRejectsUnrecordedConversationAndSyncDestination(t *testing.T) {
	store := newTestSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		cfg.SessionService = store
		return &Bridge{log: slog.New(slog.DiscardHandler), config: cfg, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	})
	runTestManager(t, manager)
	rt := &Runtime{threads: manager, Sessions: store}
	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = "missing"
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), `conversation "missing" is not recorded`)

	require.NoError(t, store.UpsertThread("web:1", ThreadState{Agent: "main"}))

	inbound.ConversationID = "web:1"
	inbound.SyncDestination = "missing-y"
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), `conversation "missing-y" is not recorded`)
}

func TestRuntimeStartGoalRecordsGoalAndQueuesKickoff(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := "web:goal"
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	cfg := &config.Config{Workspace: filepath.Join(t.TempDir(), "missing")}
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, Agent: "main", SessionService: store}, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(config.NewLockedConfig(cfg), store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)
	manager.bridges = map[string]directBridge{conversationID: bridge}
	rt := &Runtime{threads: manager, Sessions: store, Cfg: config.NewLockedConfig(cfg)}

	webGoal := func() *protocol.InboundMessage {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "ship it"}, true)
		inbound.ConversationID = conversationID

		return inbound
	}

	require.ErrorContains(t, rt.StartGoal(t.Context(), webGoal(), protocol.GoalRequest{Objective: "ship it", CheckScript: "make test", MaxTurns: 3}), "validate goal check script")

	_, exists, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, rt.StartGoal(t.Context(), webGoal(), protocol.GoalRequest{Objective: "ship it", MaxTurns: 3}))

	goal, exists, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, "ship it", goal.Objective)
	assert.Equal(t, 3, goal.MaxTurns)
	require.Len(t, bridge.requestCh, 1)
	kickoff := (<-bridge.requestCh).inbound
	assert.Equal(t, protocol.GoalActionKickoff, kickoff.GoalAction)
	assert.Equal(t, "ship it", kickoff.Text)

	require.ErrorIs(t, rt.StartGoal(t.Context(), webGoal(), protocol.GoalRequest{Objective: "again"}), protocol.ErrGoalAlreadyActive)
	assert.Empty(t, bridge.requestCh)

	require.ErrorContains(t, rt.StartGoal(t.Context(), &protocol.InboundMessage{ConversationID: "web:missing"}, protocol.GoalRequest{Objective: "x"}), `conversation "web:missing" is not recorded`)
}

func TestRuntimeQueueAndLaterWorkOps(t *testing.T) {
	store := newTestSessionService(t)
	target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)
	manager.bridges = map[string]directBridge{conversationID: bridge}
	rt := &Runtime{threads: manager, Sessions: store}

	first := &protocol.ThreadQueueItem{ID: "q1", Message: "one", Source: protocol.SourceSlack, Principal: "U1", SlackChannel: target.ChannelID, SlackTS: "1", SlackReply: &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: "1", ThreadTS: target.ThreadID}}
	second := &protocol.ThreadQueueItem{ID: "q2", Message: "two", Source: protocol.SourceSlack, Principal: "U2", SlackChannel: target.ChannelID, SlackTS: "2", SlackReply: &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: "2", ThreadTS: target.ThreadID}}

	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, first))
	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, second))

	items, err := rt.QueueItems(conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	require.NoError(t, rt.ReorderQueueItems(conversationID, []string{"q2", "q1", "missing"}))
	items, err = rt.QueueItems(conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"q2", "q1"}, []string{items[0].ID, items[1].ID})

	removed, err := rt.DeleteQueueItem(t.Context(), conversationID, "q1")
	require.NoError(t, err)
	require.True(t, removed)

	promoted, err := rt.PromoteQueueItem(t.Context(), conversationID, "q2")
	require.NoError(t, err)
	require.True(t, promoted)

	require.False(t, manager.ThreadBusy(target))
	require.NoError(t, manager.PickLaterWork(t.Context(), conversationID))
	scheduled, err := manager.ScheduledMessages(target)
	require.NoError(t, err)
	require.Empty(t, scheduled)
}

func TestRuntimeHeldQueueManualRelease(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)

	const conversationID = "web-held"
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	var logs lockedBuffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bridge := &Bridge{log: logger, config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, store, logger, func(Config) directBridge { return bridge })
	runTestManager(t, manager)
	manager.bridges = map[string]directBridge{conversationID: bridge}
	rt := &Runtime{threads: manager, Sessions: store}
	held := &protocol.ThreadQueueItem{ID: "held", Kind: protocol.InboundKindHeld, Message: "/keep this", Principal: "alice", Source: protocol.SourceWeb, Content: protocol.InboundContent{Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("image")}}}}
	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, held))
	require.Empty(t, bridge.requestCh)
	require.NoError(t, store.Stop())
	store = newTestSessionServiceAt(t, workspace)
	rt.Sessions, manager.store, bridge.config.SessionService = store, store, store
	persisted, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	require.Equal(t, held.Message, persisted[0].Message)
	require.Equal(t, held.Content.Attachments, persisted[0].Inbound.Attachments)
	require.Equal(t, held.Principal, persisted[0].Principal)
	require.Equal(t, held.Kind, persisted[0].Kind)
	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Empty(t, bridge.requestCh)
	bridge.handling = true

	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, held))
	require.NoError(t, bridge.pickLaterWork(t.Context(), true))
	require.Empty(t, bridge.requestCh)
	bridge.handling = false
	_, claimed, err := (stateDAO{db: store.db}).claimThreadQueueItem(t.Context(), conversationID, held.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	promoted, err := rt.PromoteQueueItem(t.Context(), conversationID, held.ID)
	require.NoError(t, err)
	require.False(t, promoted)

	items, err := rt.QueueItems(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, held.Message, items[0].Message)
	removed, err := rt.DeleteQueueItem(t.Context(), conversationID, held.ID)
	require.NoError(t, err)
	require.True(t, removed)

	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, held))
	require.NoError(t, store.PutThreadQueueItem("ready", &protocol.ThreadQueueItem{ConversationID: conversationID, Message: "first", Position: 40}))
	popped, err := rt.PopQueueItem(t.Context(), "other", held.ID)
	require.NoError(t, err)
	require.False(t, popped)

	results := make([]bool, 2)

	var pops errgroup.Group
	for i := range results {
		pops.Go(func() error {
			var err error

			results[i], err = rt.PopQueueItem(t.Context(), conversationID, held.ID)

			return err
		})
	}

	require.NoError(t, pops.Wait())
	require.NotEqual(t, results[0], results[1])
	popped, err = rt.PopQueueItem(t.Context(), conversationID, held.ID)
	require.NoError(t, err)
	require.False(t, popped)

	items, err = rt.QueueItems(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "ready", items[0].ID)

	require.Equal(t, held.ID, items[1].ID)
	require.Equal(t, protocol.InboundKindEnqueue, items[1].Kind)
	require.Equal(t, 41, items[1].Position)
	require.Equal(t, "ready", (<-bridge.requestCh).queueItemID)
	_, claimed, err = (stateDAO{db: store.db}).claimThreadQueueItem(t.Context(), conversationID, "ready")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	request := <-bridge.requestCh
	require.Equal(t, held.ID, request.queueItemID)
	require.Equal(t, protocol.InboundKindEnqueue, request.inbound.Kind)
	require.Equal(t, held.ID, request.inbound.Metadata["web_message_id"])
	require.Equal(t, held.Message, request.inbound.Text)
	require.Equal(t, held.Content.Attachments, request.inbound.Attachments)
	require.Equal(t, held.Principal, request.inbound.Metadata[protocol.InboundPrincipalMetadataKey])
	require.Equal(t, protocol.SourceWeb, request.inbound.Source)
	require.Equal(t, conversationID, request.inbound.ConversationID)
	outbound := bridge.newOutboundMessage(request.inbound, "turn", "reply", true)
	require.Equal(t, conversationID, outbound.ConversationID)
	require.Nil(t, outbound.SlackReply)
	promoted, err = rt.PromoteQueueItem(t.Context(), conversationID, held.ID)
	require.NoError(t, err)
	require.True(t, promoted)
	require.Equal(t, protocol.InboundKindSteer, (<-bridge.requestCh).inbound.Kind)
	require.Contains(t, logs.String(), `"event":"queue_removed"`)
	require.Contains(t, logs.String(), `"blocker":"active_or_queued_request"`)
	require.NotContains(t, logs.String(), held.Message)
}

func TestAttachSlack(t *testing.T) {
	manager := newThreadBridgeManager(new(config.LockedConfig), nil, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return nil
	})
	runTestManager(t, manager)

	var asker protocol.UserQuestionAsker

	slack := new(slackFrontendMock)
	rt := &Runtime{threads: manager, slackAsker: &asker}
	rt.AttachSlack(slack)
	require.True(t, asker.ExposeTool())
	require.Same(t, slack, manager.cronRoots)
}

// The turn learns, after the calls' background results, which of its calls moved.
func TestMoveToBackgroundWithdrawsQuestionAndMovesSubagent(t *testing.T) {
	release := make(chan struct{})
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		switch {
		case n == 0:
			code := "def main():\n    return 'after: ' + ask_user_question(question='Ship?', details='', options=[], multiple=False)\n"
			execute, _ := json.Marshal(struct {
				Code        string `json:"code"`
				Description string `json:"description"`
				Background  bool   `json:"background"`
			}{code, "ask", false}) // Encoding strings and a bool cannot fail.
			task, _ := json.Marshal(struct {
				Description  string `json:"description"`
				Prompt       string `json:"prompt"`
				SubagentType string `json:"subagent_type"`
				Background   bool   `json:"background"`
				Continue     string `json:"continue"`
			}{"research", "research the flaky test", "researcher", false, ""})
			_, _ = fmt.Fprintf(w, `{"id":"resp_0","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_e","name":"execute","arguments":%q},{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_t","name":"task","arguments":%q}]}`, execute, task)
		case strings.Contains(body, `"content":"research the flaky test"`):
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}

			_, _ = w.Write([]byte(`{"id":"resp_s","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_s","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"found it","annotations":[]}]}]}`))
		default:
			answerNoteTest(w, r, n, body)
		}
	})
	releaseSubagent := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSubagent) // Before the provider closes, which waits for the held request.
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmode: primary\nmodel: gpt-5.5\npermission:\n  task: allow\n  rocketclaw:\n    allow_background: allow\n    ask_user_question: allow\n---\nPrompt\n")

	asked, withdrawn := make(chan string, 1), make(chan error, 1)
	asker := protocol.InteractiveUserQuestionAsker(func(ctx context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		asked <- req.ID

		<-ctx.Done() // The Slack connector deletes an unanswered question when its wait is cancelled.

		withdrawn <- context.Cause(ctx)

		return protocol.AskUserQuestionAnswer{}, ctx.Err()
	})

	var (
		manager  *threadBridgeManager
		registry *backgroundRegistry
	)

	manager = newThreadBridgeManager(config.NewLockedConfig(nt.cfg), nt.service, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		cfg.SessionService, cfg.RequestRestart, cfg.StartNewThread, cfg.UserQuestionAsker = nt.service, testNoopRestart, testNoopStartNewThread, asker
		bridge := NewConversation(config.NewLockedConfig(nt.cfg), finalsPublisher{finals: nt.finals}, &cfg, slog.New(slog.DiscardHandler))
		bridge.threads, bridge.background = manager, registry

		return bridge
	})
	registry = newBackgroundRegistry(nt.service, manager, testLogger())
	runTestManager(t, manager)

	rt := &Runtime{threads: manager, Sessions: nt.service, background: registry}
	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("ship it")))

	executeID, _, _ := strings.Cut(<-asked, "/host/")
	taskID := strings.TrimSuffix(executeID, "call_e") + "call_t"

	for range 2 { // The turn's calls and the subagent's run.
		nt.request(t)
	}

	_, movable, err := rt.BackgroundJobs(t.Context(), nt.conversationID)
	require.NoError(t, err)
	require.True(t, movable)

	moved, err := rt.MoveToBackground(nt.conversationID)
	require.NoError(t, err)
	require.True(t, moved)

	select {
	case cause := <-withdrawn:
		require.ErrorIs(t, cause, errMovedToBackground, "the pending question is withdrawn")
	case <-time.After(10 * time.Second):
		t.Fatal("the pending question was not withdrawn")
	}

	body := nt.request(t)
	assert.Contains(t, body, "The script is running in the background (job ID: "+executeID+")")
	assert.Contains(t, body, "The subagent is working in the background (job ID: "+taskID+")")
	assert.Equal(t, "[System]\n\n"+fmt.Sprintf(movedNote, "- execute: ask (job ID: "+executeID+")\n- task: research (job ID: "+taskID+")\n"), lastRequestMessage(t, body))
	readFinal(t, nt.finals)

	jobs := func() []string {
		rows, err := queryStrings(context.Background(), nt.service.db, `SELECT job_id || ' ' || status || ' ' || note_state || ' ' || result FROM background_jobs ORDER BY job_id`, "background rows")
		require.NoError(t, err)

		return rows
	}

	require.Eventually(t, func() bool {
		rows := jobs()
		return len(rows) == 2 && rows[0] == executeID+" completed consumed after: "+questionWithdrawn && strings.HasPrefix(rows[1], taskID+" running none")
	}, 10*time.Second, 10*time.Millisecond, "the script finishes in the background while the moved subagent runs")

	releaseSubagent()
	require.Eventually(t, func() bool {
		rows := jobs()
		return len(rows) == 2 && strings.HasPrefix(rows[1], taskID+" completed consumed") && strings.Contains(rows[1], "found it")
	}, 10*time.Second, 10*time.Millisecond, "the moved subagent finishes and reports")
	nt.waitIdle(t, manager, nt.conversationID)
}

// Nothing to move is a no-op, and a denied agent's running execute stays in the foreground.
func TestMoveToBackgroundSkipsDeniedAgent(t *testing.T) {
	gate := ""
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if n > 0 {
			answerNoteTest(w, r, n, body)
			return
		}

		code := "def main():\n    bash(command=r'''touch " + gate + ".started; while [ ! -f " + gate + " ]; do sleep 0.05; done''')\n    return 'done'\n"
		arguments, _ := json.Marshal(struct {
			Code string `json:"code"`
		}{code}) // Encoding a string cannot fail.
		_, _ = fmt.Fprintf(w, `{"id":"resp_0","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_e","name":"execute","arguments":%q}]}`, arguments)
	})
	gate = filepath.Join(nt.cfg.Workspace, "gate")
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmode: primary\nmodel: gpt-5.5\npermission:\n  bash: {\"*\": allow}\n---\nPrompt\n")

	manager, _ := nt.run(t, ignoreOutbound)
	bridge := nt.bridge(t, manager, nt.conversationID)
	rt := &Runtime{threads: manager, Sessions: nt.service, background: bridge.background}

	moved, err := rt.MoveToBackground(nt.conversationID)
	require.NoError(t, err)
	require.False(t, moved, "an idle conversation has nothing to move")

	require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("run it")))
	require.Eventually(t, func() bool {
		_, err := os.Stat(gate + ".started")
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)

	jobs, movable, err := rt.BackgroundJobs(t.Context(), nt.conversationID)
	require.NoError(t, err)
	require.Empty(t, jobs)
	require.False(t, movable, "a denied agent's call is not movable")

	moved, err = rt.MoveToBackground(nt.conversationID)
	require.NoError(t, err)
	require.False(t, moved)

	bridge.mu.Lock()
	require.Empty(t, bridge.movedNotes, "no note tells the turn of a move")
	bridge.mu.Unlock()

	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	nt.request(t)
	assert.Contains(t, nt.request(t), `done`, "the call returns its real result")
	readFinal(t, nt.finals)
	assert.Empty(t, backgroundRows(t, nt.service))
}

// A move racing a finish gives the call one outcome: its real result, or a background result whose note holds it.
func TestMoveAsCallFinishes(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	finished := &backgroundWorkMock{RunBackgroundFunc: func(context.Context) (string, error) { return "done", nil }, DetachFunc: func() bool { return true }}

	result, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindExecute}, finished)
	require.NoError(t, err)
	require.Equal(t, rocketcode.BackgroundResult{Output: "done"}, result)
	require.Empty(t, registry.move("main"), "a finished call has nothing left to move")

	for i := range 20 {
		jobID := fmt.Sprintf("turn-2/call/%d", i)
		started, finish := make(chan struct{}, 1), make(chan struct{})

		var call errgroup.Group
		call.Go(func() (err error) {
			result, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: jobID, Kind: rocketcode.BackgroundKindExecute}, blockedWork(started, finish))
			return err
		})
		<-started
		close(finish)
		registry.move("main")
		require.NoError(t, call.Wait())

		exists, err := store.hasBackgroundJob(t.Context(), "main", jobID)
		require.NoError(t, err)
		require.Equal(t, result.Moved, exists)

		if !result.Moved {
			require.Equal(t, "done", result.Output)
			continue
		}

		require.Equal(t, "main", <-notes)
		require.Equal(t, "done", testBackgroundRow(t, store, "main", jobID).result, "the note carries the real result")
	}
}

// Hidden runs' jobs are listed and stoppable at their destination; a killed job stays listed until its note is consumed.
func TestRuntimeListsAndStopsBackgroundJobs(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	rt := &Runtime{Sessions: store, background: registry}
	started, release := make(chan struct{}, 1), make(chan struct{})

	defer close(release)

	cron := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "cron:daily", origin: &protocol.InboundMessage{SyncDestination: "reports"}}
	_, err := cron.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/loop", Kind: rocketcode.BackgroundKindTask, Label: "loop", CallID: "loop", SubagentKey: "/loop", Detached: true}, blockedWork(started, release))
	require.NoError(t, err)
	<-started

	for _, jobID := range []string{"turn-2/call/killed", "turn-2/call/consumed"} {
		job := testBackgroundJob("reports", jobID)
		createTestBackgroundJob(t, store, job)
		finishTestBackgroundJob(t, store, job, backgroundKilled, false)
	}

	_, err = store.db.ExecContext(t.Context(), `UPDATE background_jobs SET note_state = $1 WHERE job_id = 'turn-2/call/consumed'`, noteConsumed)
	require.NoError(t, err)

	loop := protocol.BackgroundJob{ID: "turn-1/call/loop", Kind: "task", State: "running", Label: "loop", ToolCallID: "loop", SubagentKey: "/loop", Hidden: true}
	killed := protocol.BackgroundJob{ID: "turn-2/call/killed", Kind: "execute", State: "killed", Label: "tests", ToolCallID: "call"}
	jobs, movable, err := rt.BackgroundJobs(t.Context(), "reports")
	require.NoError(t, err)
	require.False(t, movable)
	require.Equal(t, []protocol.BackgroundJob{loop, killed}, jobs)

	stopped, err := rt.StopBackgroundJob(t.Context(), "reports", loop.ID)
	require.NoError(t, err)
	require.True(t, stopped)
	require.Equal(t, "cron:daily", <-notes, "the stopped note wakes the hidden run")

	loop.State, loop.StoppedBy = "stopped", "user"
	_, err = store.db.ExecContext(t.Context(), `UPDATE background_jobs SET note_state = $1 WHERE job_id = $2`, noteConsumed, killed.ID)
	require.NoError(t, err)
	jobs, _, err = rt.BackgroundJobs(t.Context(), "reports")
	require.NoError(t, err)
	require.Equal(t, []protocol.BackgroundJob{loop}, jobs, "a consumed note drops its job from the list")
}

// An attached call's start and finish flip movable, which no row change reports.
func TestMovableChangeWakesWebViews(t *testing.T) {
	store := newTestSessionService(t)
	registry, _ := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	ctx, cancel := context.WithCancel(t.Context())
	changes := make(chan protocol.ConversationChange, 4)

	var listen errgroup.Group
	listen.Go(func() error {
		for change, err := range store.Changes(ctx, "main") {
			if err != nil {
				return err
			}

			changes <- change
		}

		return nil
	})
	t.Cleanup(func() {
		cancel()
		require.NoError(t, listen.Wait())
	})

	<-changes // The listener's opening wake-up.

	next := func() protocol.ConversationChange {
		select {
		case change := <-changes:
			return change
		case <-time.After(5 * time.Second):
			t.Fatal("no change notice")
			return protocol.ConversationChange{}
		}
	}

	started, release := make(chan struct{}, 1), make(chan struct{})

	var call errgroup.Group
	call.Go(func() error {
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindExecute}, blockedWork(started, release))
		return err
	})
	<-started
	assert.NotEmpty(t, next().Revision, "the attached call made the conversation movable")

	close(release)
	require.NoError(t, call.Wait())
	assert.Equal(t, "main", next().ConversationID, "the finished call made it unmovable")
}
