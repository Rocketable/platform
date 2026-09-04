package backend

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

func TestRuntimeSubscribeIsLiveAndWaitsForDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := new(Runtime)
		history := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "old")
		require.NoError(t, rt.PublishOutbound(t.Context(), history))
		events := rt.Subscribe(t.Context())
		message := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "new")
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

func TestRuntimeProducerKeepsDestinationUntilSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionService(t)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		rt := &Runtime{Sessions: store, Cfg: new(config.Config), Log: slog.New(slog.DiscardHandler)}

		rt.threads = newThreadBridgeManager(rt.Cfg, store, rt.Log, func(cfg Config) directBridge {
			cfg.SessionService = store
			return NewConversation(rt.Cfg, rt, &cfg, rt.Log)
		})
		defer func() { require.NoError(t, rt.threads.Stop()) }()

		for _, id := range []string{"X", "Y"} {
			require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
			replay, err := replayInputForMessage("user", id+" history")
			require.NoError(t, err)
			_, err = store.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: replay})
			require.NoError(t, err)
		}

		events := rt.Subscribe(ctx)

		var delivered []string

		go func() {
			for event := range events {
				delivered = append(delivered, event.Message.ConversationID)
				event.Acknowledgement <- nil
			}
		}()

		producer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "producer", "", false)
		producer.ConversationID, producer.SyncDestination = "X", "Y"
		producer.HadAttachments, producer.HadNonImageAttachments = true, true
		require.NoError(t, rt.RunTurn(ctx, producer))
		source := rt.threads.bridges["X"].bridge.(*Bridge)
		source.mu.Lock()
		source.activeReply = producer
		source.mu.Unlock()
		require.NoError(t, source.ScheduleMessage(time.Hour, "after sync", false))

		scheduled, err := store.ScheduledMessagesForConversation("X")
		require.NoError(t, err)
		require.Empty(t, scheduled)

		var waiting errgroup.Group
		waiting.Go(func() error {
			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindEnqueue, "human", "", true)
			inbound.ConversationID = "Y"
			inbound.HadAttachments, inbound.HadNonImageAttachments = true, true

			return rt.RunTurn(ctx, inbound)
		})
		synctest.Wait()
		require.Equal(t, []string{"X"}, delivered)

		failedSync, cancelSync := context.WithCancel(ctx)
		cancelSync()
		require.ErrorIs(t, rt.SyncConversation(failedSync, "X", "Y"), context.Canceled)
		synctest.Wait()
		require.Equal(t, []string{"X"}, delivered)
		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))
		require.NoError(t, waiting.Wait())
		synctest.Wait()
		require.Equal(t, []string{"X", "Y", "Y"}, delivered)
		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))
		synctest.Wait()
		require.Len(t, delivered, 3)

		entries, err := store.ObserveEntries(ctx, "Y", 0)
		require.NoError(t, err)
		require.Len(t, entries, 3)
		messages, err := replayInputMessages(entries[0].Entry.ReplayInput)
		require.NoError(t, err)
		require.Equal(t, "Y history", messages[0].text)

		entries, err = store.ObserveEntries(ctx, "X", 0)
		require.NoError(t, err)
		require.Len(t, entries, 2)
		sourceEntries := entries

		scheduled, err = store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Len(t, scheduled, 1)

		for _, message := range scheduled {
			require.Equal(t, "after sync", message.Message)
		}

		// These attachment-fallback turns exercise runtime routing, not provider
		// continuation. Seed the Y-only reply because fallback does not record it.
		reply := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "human", "", true)
		reply.ConversationID = "Y"
		reply.HadAttachments, reply.HadNonImageAttachments = true, true
		require.NoError(t, rt.RunTurn(ctx, reply))

		replay, err := replayInputForMessage("user", "Y-only reply after sync")
		require.NoError(t, err)
		_, err = store.AppendEntryID(ctx, "Y", &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: replay})
		require.NoError(t, err)
		destinationEntries, err := store.ObserveEntries(ctx, "Y", 0)
		require.NoError(t, err)
		require.Len(t, destinationEntries, 4)

		continuation := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "producer", "", false)
		continuation.ConversationID, continuation.SyncDestination = "X", "Y"
		continuation.HadAttachments, continuation.HadNonImageAttachments = true, true
		require.NoError(t, rt.RunTurn(ctx, continuation))
		synctest.Wait()
		require.Equal(t, []string{"X", "Y", "Y", "Y", "X"}, delivered)

		entries, err = store.ObserveEntries(ctx, "X", 0)
		require.NoError(t, err)
		require.Equal(t, sourceEntries, entries, "Y history and reply must stay off X")
		entries, err = store.ObserveEntries(ctx, "Y", 0)
		require.NoError(t, err)
		require.Equal(t, destinationEntries, entries, "continuing X must not copy entries without another Sync")

		scheduledAfter, err := store.ScheduledMessagesForConversation("Y")
		require.NoError(t, err)
		require.Equal(t, scheduled, scheduledAfter, "continuing X must not duplicate copied effects")

		require.NoError(t, rt.SyncConversation(ctx, "X", "Y"))
		synctest.Wait()
		require.Equal(t, []string{"X", "Y", "Y", "Y", "X", "Y"}, delivered)

		entries, err = store.ObserveEntries(ctx, "X", 0)
		require.NoError(t, err)
		require.Equal(t, sourceEntries, entries, "subsequent Sync must not copy Y history or reply back to X")
		entries, err = store.ObserveEntries(ctx, "Y", 0)
		require.NoError(t, err)
		require.Equal(t, destinationEntries, entries, "subsequent Sync must not duplicate copied entries")

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

				rt := &Runtime{Sessions: store, Cfg: new(config.Config), Log: slog.New(slog.DiscardHandler)}

				rt.threads = newThreadBridgeManager(rt.Cfg, store, rt.Log, func(cfg Config) directBridge {
					cfg.SessionService = store
					return NewConversation(rt.Cfg, rt, &cfg, rt.Log)
				})
				defer func() { require.NoError(t, rt.threads.Stop()) }()

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
						delivered = append(delivered, event.Message.ConversationID)
						order = append(order, event.Message.SlackReply.MessageTS)

						event.Acknowledgement <- nil
					}

					return nil
				})

				producer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "producer", "", false)

				producer.ConversationID, producer.SyncDestination = "X", destination
				producer.HadAttachments, producer.HadNonImageAttachments = true, true
				producer.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "producer"}
				require.NoError(t, rt.RunTurn(ctx, producer))

				var waiting errgroup.Group
				waiting.Go(func() error {
					steer := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "", "", true)

					steer.ConversationID = destination
					steer.HadAttachments, steer.HadNonImageAttachments = true, true
					steer.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "steer"}

					return rt.RunTurn(ctx, steer)
				})
				synctest.Wait()

				competing := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "competing", "", false)
				competing.ConversationID, competing.SyncDestination = tt.producerID, destination
				competing.HadAttachments, competing.HadNonImageAttachments = true, true
				competing.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "competing"}

				for _, enqueue := range []bool{tt.enqueueFirst, !tt.enqueueFirst} {
					if enqueue {
						content := protocol.InboundContent{HadAttachments: true, HadNonImageAttachments: true}
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

				source := rt.threads.bridges["X"].bridge.(*Bridge)
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

		rt := &Runtime{Sessions: store, Cfg: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, Log: slog.New(slog.DiscardHandler)}
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
		defer func() { require.NoError(t, rt.threads.Stop()) }()

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
			inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "", text, true)
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
				bridge := rt.threads.bridges["Y"].bridge.(*Bridge)
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
	bridge := &Bridge{inputOpen: true, requestCh: make(chan bridgeRequest, 2)}

	for _, text := range []string{"first", "second"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, "", &protocol.InboundContent{Text: text, Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte(text)}}}, true)
		inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "U1"}
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	drain := rocketcode.SteerDrain{Fn: bridge.drainSteers}
	inputs := drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 2)

	for i, text := range []string{"first", "second"} {
		require.Contains(t, inputs[i].Text, text)
		require.Contains(t, inputs[i].Text, "U1")
		require.Equal(t, []rocketcode.Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64," + []string{"Zmlyc3Q=", "c2Vjb25k"}[i]}}, inputs[i].Attachments)
	}

	require.True(t, bridge.inputOpen, "injected input reopens provider work")
	require.Empty(t, drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer))
	require.False(t, bridge.inputOpen)

	late := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, "", &protocol.InboundContent{Text: "late", Attachments: []protocol.InboundAttachment{{Name: "late.png", MIMEType: "image/png", Data: []byte("late")}}}, true)
	late.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "U2"}
	require.NoError(t, bridge.Submit(t.Context(), late))
	require.Empty(t, drain.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer))
	require.Same(t, late, (<-bridge.requestCh).inbound, "cutoff steer keeps its full input for the next turn")
}

func TestThreadBridgeManagerWaitingSteerControls(t *testing.T) {
	store := newTestSessionService(t)
	target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	bridge := &Bridge{config: Config{ConversationID: conversationID, SessionService: store}, inputOpen: true, requestCh: make(chan bridgeRequest, 2)}
	manager := &threadBridgeManager{store: store, bridges: map[string]*managedThreadBridge{conversationID: {bridge: bridge}}}
	active := &turnCompletion{done: make(chan struct{})}
	bridge.activeCompletion = active

	for _, text := range []string{"first", "drop", "last"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindSteer, "", &protocol.InboundContent{Text: text, Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte(text)}}}, true)
		inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: text + " author"}
		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: text}
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	items, err := manager.ThreadQueueItems(t.Context(), target)
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

	itemsAfter, err := manager.ThreadQueueItems(t.Context(), target)
	require.NoError(t, err)
	require.Empty(t, itemsAfter)

	for _, item := range items {
		removed, err := manager.DeleteThreadQueueItem(t.Context(), target, item.ID)
		require.NoError(t, err)
		require.False(t, removed, "consumed or dropped steer cannot be dropped again")
	}

	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	content := protocol.InboundContent{Text: "promoted", TextAttachments: []string{"acquired text file", "acquired forwarded thread"}, Attachments: []protocol.InboundAttachment{{Name: "original.png", MIMEType: "image/png", Data: []byte("original")}}}
	queued := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindEnqueue, "", &content, true)
	queued.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "original author"}
	queued.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, ThreadTS: target.ThreadID, MessageTS: "promoted", RecipientTeamID: "T1", RecipientUserID: "U1"}
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q1", Message: "promoted", Content: content, Source: queued.Source, SlackReply: queued.SlackReply, Principal: "original author", SlackChannel: target.ChannelID, SlackTS: "promoted"}))

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
	itemsAfter, err = manager.ThreadQueueItems(t.Context(), target)
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
}

type stubSlack struct{}

func (stubSlack) HandleBroadcast(context.Context, *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastDropped}
}
func (stubSlack) Start(context.Context) error { return nil }
func (stubSlack) Stop(context.Context) error  { return nil }
func (stubSlack) SendResponse(context.Context, *protocol.OutboundMessage) error {
	return nil
}
func (stubSlack) AbortResponse(*protocol.OutboundMessage) {}
func (stubSlack) StartNewThreadRoot(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
	return protocol.StartNewThreadRootResult{}, nil
}
func (stubSlack) AskUserQuestion(context.Context, *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
	return protocol.AskUserQuestionAnswer{}, nil
}
func (stubSlack) DrainSteers(context.Context, string) []string { return nil }
func (stubSlack) ActivateEnqueue(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
	return nil
}
func (stubSlack) SetPendingSteersSink(protocol.PendingSteersSink)      {}
func (stubSlack) RestorePendingSteers(string, []protocol.PendingSteer) {}
func (stubSlack) DiscardPendingSteers(context.Context, []protocol.PendingSteer) {
}

func TestRuntimeRunTurnCancelPublishesEmptyComplete(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.0")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := &Bridge{config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
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

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindCancel, "", "", true)
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
	canceled := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindCancel, "", "", true)
	canceled.ConversationID = conversationID
	require.ErrorIs(t, rt.RunTurn(t.Context(), canceled), context.Canceled)
}

func TestRuntimeRunTurnRejectsUnrecordedConversationAndSyncDestination(t *testing.T) {
	store := newTestSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		cfg.SessionService = store
		return &Bridge{config: cfg, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	})
	rt := &Runtime{threads: manager, Sessions: store}
	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = "missing"
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), `conversation "missing" is not recorded`)

	require.NoError(t, store.UpsertThread("web:1", ThreadState{Agent: "main"}))

	inbound.ConversationID = "web:1"
	inbound.SyncDestination = "missing-y"
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), `conversation "missing-y" is not recorded`)
}

func TestRuntimeQueueAndLaterWorkOps(t *testing.T) {
	store := newTestSessionService(t)
	target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := &Bridge{config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	manager.bridges = map[string]*managedThreadBridge{conversationID: {bridge: bridge}}
	rt := &Runtime{threads: manager, Sessions: store}

	first := &protocol.ThreadQueueItem{ID: "q1", Message: "one", Source: protocol.SourceSlack, Principal: "U1", SlackChannel: target.ChannelID, SlackTS: "1", SlackReply: &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: "1", ThreadTS: target.ThreadID}}
	second := &protocol.ThreadQueueItem{ID: "q2", Message: "two", Source: protocol.SourceSlack, Principal: "U2", SlackChannel: target.ChannelID, SlackTS: "2", SlackReply: &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: "2", ThreadTS: target.ThreadID}}

	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, first))
	require.NoError(t, rt.StashQueueItem(t.Context(), conversationID, second))

	items, err := rt.QueueItems(t.Context(), conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	require.NoError(t, rt.ReorderQueueItems(t.Context(), conversationID, []string{"q2", "q1", "missing"}))
	items, err = rt.QueueItems(t.Context(), conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"q2", "q1"}, []string{items[0].ID, items[1].ID})

	removed, err := rt.DeleteQueueItem(t.Context(), conversationID, "q1")
	require.NoError(t, err)
	require.True(t, removed)

	promoted, err := rt.PromoteQueueItem(t.Context(), conversationID, "q2")
	require.NoError(t, err)
	require.True(t, promoted)

	require.False(t, manager.ThreadBusy(target))
	require.NoError(t, manager.PickQueuedWork(t.Context(), target))
	scheduled, err := manager.ScheduledMessages(t.Context(), target)
	require.NoError(t, err)
	require.Empty(t, scheduled)

	release, reserved, err := manager.ReserveWorkflowTurn(target)
	require.NoError(t, err)
	require.True(t, reserved)
	release()

	require.ErrorContains(t, rt.SubmitExternalMCP(t.Context(), "main", " ", newThreadInboundMessage("reply", "222.333", "111.222"), NoopActivationHook), "text thread conversation ID is required")
}

func TestAttachSlackAndSubmitExternalMCP(t *testing.T) {
	manager := newThreadBridgeManager(new(config.Config), nil, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return nil
	})

	var (
		asker protocol.UserQuestionAsker
		drain func(context.Context, string, rocketcode.TurnPhase) []string
		root  func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	)

	rt := &Runtime{threads: manager, slackAsker: &asker, drainSlack: &drain, startThreadRoot: &root}
	rt.AttachSlack(stubSlack{})
	require.True(t, asker.ExposeTool())
	require.Empty(t, drain(t.Context(), "c", 0))
	got, err := root(t.Context(), &protocol.StartNewThreadRequest{})
	require.NoError(t, err)
	require.Equal(t, protocol.StartNewThreadRootResult{}, got)
}
