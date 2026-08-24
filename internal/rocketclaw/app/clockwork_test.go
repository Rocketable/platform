package app

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clockworkTestOperation struct{ value string }

func (clockworkTestOperation) RequestKind() events.RequestKind { return "test" }

type clockworkTestPayload struct{ value string }

func (clockworkTestPayload) ResponseKind() events.ResponseKind { return events.ResponseResult }

type clockworkTestBridge struct {
	received chan *events.Broadcast
	status   events.BroadcastStatus
}

func (b *clockworkTestBridge) HandleBroadcast(_ context.Context, broadcast *events.Broadcast) events.BroadcastAcknowledgement {
	b.received <- broadcast

	return events.BroadcastAcknowledgement{Status: b.status}
}

type blockingClockworkTestBridge struct {
	received chan *events.Broadcast
	release  chan struct{}
}

func runClockwork(ctx context.Context, t *testing.T, clockwork *clockwork, handler func(context.Context, events.Request)) {
	t.Helper()

	go func() {
		if err := clockwork.run(ctx, handler); err != nil {
			t.Errorf("clockwork.run: %v", err)
		}
	}()
}

func (b *blockingClockworkTestBridge) HandleBroadcast(ctx context.Context, broadcast *events.Broadcast) events.BroadcastAcknowledgement {
	b.received <- broadcast

	select {
	case <-b.release:
		return events.BroadcastAcknowledgement{Status: events.BroadcastHandled}
	case <-ctx.Done():
		return events.BroadcastAcknowledgement{Status: events.BroadcastFailed, Err: ctx.Err()}
	}
}

func TestClockworkRequestWaitsForReceiverAndUsesOwnResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		response := make(chan events.Response)
		request := events.Request{Operation: clockworkTestOperation{value: "request"}, Response: response}
		sent := make(chan struct{})

		go func() {
			channels.Requests <- request

			close(sent)
		}()

		synctest.Wait()

		select {
		case <-sent:
			t.Fatal("request send completed before a receiver started")
		default:
		}

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			operation := request.Operation.(clockworkTestOperation)
			request.Response <- events.Response{Payload: clockworkTestPayload(operation)}
		})

		synctest.Wait()
		<-sent

		got := <-response
		payload, ok := got.Payload.(clockworkTestPayload)
		require.True(t, ok)
		require.Equal(t, "request", payload.value)
		require.NoError(t, got.Err)

		cancel()
		synctest.Wait()
	})
}

func TestClockworkRoutesResponsesToTheirRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			operation := request.Operation.(clockworkTestOperation)
			request.Response <- events.Response{Payload: clockworkTestPayload(operation)}
		})

		firstResponse := make(chan events.Response)
		secondResponse := make(chan events.Response)

		channels.Requests <- events.Request{Operation: clockworkTestOperation{value: "first"}, Response: firstResponse}

		channels.Requests <- events.Request{Operation: clockworkTestOperation{value: "second"}, Response: secondResponse}

		first := <-firstResponse
		second := <-secondResponse
		firstPayload, ok := first.Payload.(clockworkTestPayload)
		require.True(t, ok)
		secondPayload, ok := second.Payload.(clockworkTestPayload)
		require.True(t, ok)
		require.Equal(t, "first", firstPayload.value)
		require.Equal(t, "second", secondPayload.value)

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterUsesRequestChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			operation := request.Operation.(*events.TextRequest)
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Handled: operation.Kind == events.RequestTextSubmitThreadReply}}
		})

		router := newRequestTextRouter(channels.Requests)
		handled, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
		require.NoError(t, err)
		require.True(t, handled)

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterRoutesOperations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			operation := request.Operation.(*events.TextRequest)

			result := &events.TextResponse{Kind: events.ResponseResult}
			switch operation.Kind {
			case events.RequestTextReserveWorkflowTurn:
				result.Reserved = true
				result.Release = make(chan struct{}, 1)
			case events.RequestTextWorkflowDescriptions:
				result.Descriptions = []workflow.Description{{Name: "workflow"}}
			case events.RequestTextInterruptConversation, events.RequestTextInterruptThread:
				result.Inbound = events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "stopped", true)
			case events.RequestTextRegisterThread:
				result.Created = true
			case events.RequestTextThreadAgent:
				result.Agent, result.Handled = "main", true
			case events.RequestTextSwitchThreadAgent, events.RequestTextSubmitThreadReply, events.RequestTextSubmitWhenActive:
				result.Handled = true
			case events.RequestTextStartThread, events.RequestTextStartGoal, events.RequestTextStartWorkflow, events.RequestTextRegisterCronThread, events.RequestTextSubmitExternalMCP, events.RequestTextStashThreadQueue, events.RequestTextListThreadQueue, events.RequestTextReorderThreadQueue, events.RequestTextDeleteThreadQueueItem, events.RequestTextListScheduledMessages, events.RequestTextDeleteScheduledMessage, events.RequestTextResetScheduledMessages:
			}

			request.Response <- events.Response{Payload: result}
		})

		router := newRequestTextRouter(channels.Requests)
		target := events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}
		require.NoError(t, router.StartThread(t.Context(), "main", target, nil))
		require.NoError(t, router.StartGoalInThread(t.Context(), "main", "goal", "", 1, target, nil))
		require.NoError(t, router.StartWorkflowInThread(t.Context(), "main", "workflow", "args", target, nil))
		release, reserved, err := router.ReserveWorkflowTurn(target)
		require.NoError(t, err)
		require.True(t, reserved)
		release()

		descriptions, err := router.WorkflowDescriptions()
		require.NoError(t, err)
		require.Equal(t, "workflow", descriptions[0].Name)
		require.NotNil(t, router.InterruptConversation("conversation"))
		require.NotNil(t, func() *events.InboundMessage {
			result, err := router.InterruptThread(target)
			require.NoError(t, err)

			return result
		}())

		created, err := router.RegisterThread(target, "main")
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, router.RegisterCronThread(t.Context(), target, "main"))
		agent, handled, err := router.ThreadAgent(target)
		require.NoError(t, err)
		require.Equal(t, "main", agent)
		require.True(t, handled)
		handled, err = router.SwitchThreadAgent(target, "planner")
		require.NoError(t, err)
		require.True(t, handled)
		handled, err = router.SubmitThreadReply(t.Context(), target, nil)
		require.NoError(t, err)
		require.True(t, handled)
		handled, err = router.SubmitWhenActive(t.Context(), target, nil, harnessbridge.NoopActivationHook)
		require.NoError(t, err)
		require.True(t, handled)
		require.NoError(t, router.StashThreadQueueItem(t.Context(), target, &harnessbridge.ThreadQueueItem{ID: "q1", Message: "later"}))
		_, err = router.ThreadQueueItems(t.Context(), target)
		require.NoError(t, err)
		require.NoError(t, router.ReorderThreadQueue(t.Context(), target, []string{"q1"}))
		require.NoError(t, router.DeleteThreadQueueItem(t.Context(), target, "q1"))
		_, err = router.ScheduledMessages(t.Context(), target)
		require.NoError(t, err)
		require.NoError(t, router.DeleteScheduledMessage(t.Context(), target, "s1"))
		require.NoError(t, router.ResetScheduledMessages(t.Context(), target))
		require.Equal(t, harnessbridge.ThreadTurnUnclassified, router.TurnPhase(target))

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterConsumesProgressAndFinalOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		outputs := make(chan *events.OutboundMessage, 2)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Handled: true}}

			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseProgress, Message: events.NewOutboundMessage(events.SourceSlack, "conversation", "progress")}}

			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Message: events.NewOutboundMessage(events.SourceSlack, "conversation", "final")}}
		})

		router := newRequestTextRouter(channels.Requests)
		router.output = func(_ context.Context, message *events.OutboundMessage) error {
			outputs <- message

			return nil
		}
		_, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
		require.NoError(t, err)
		require.Equal(t, "progress", (<-outputs).Text)
		require.Equal(t, "final", (<-outputs).Text)

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterAbortsFailedCompleteOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		aborted := make(chan *events.OutboundMessage, 1)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			message := events.NewOutboundMessage(events.SourceSlack, "conversation", "final")

			message.Complete = true
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Message: message}}
		})

		router := newRequestTextRouter(channels.Requests)
		router.output = func(context.Context, *events.OutboundMessage) error { return context.Canceled }
		router.abort = func(message *events.OutboundMessage) { aborted <- message }
		_, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil)
		require.Error(t, err)
		require.Equal(t, "final", (<-aborted).Text)

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterAbortsFailedProgressAndContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		aborted := make(chan string, 1)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Handled: true}}

			progress := events.NewOutboundMessage(events.SourceSlack, "conversation", "progress")
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseProgress, Message: progress}}

			final := events.NewOutboundMessage(events.SourceSlack, "conversation", "final")

			final.Complete = true
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Message: final}}
		})

		router := newRequestTextRouter(channels.Requests)
		router.output = func(_ context.Context, message *events.OutboundMessage) error {
			if message.Text == "progress" {
				return context.Canceled
			}

			return nil
		}
		router.abort = func(message *events.OutboundMessage) { aborted <- message.Text }
		_, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
		require.NoError(t, err)
		require.Equal(t, "progress", <-aborted)

		cancel()
		synctest.Wait()
	})
}

func TestRequestTextRouterConsumeOutputAbortsFailedMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		aborted := make(chan string, 2)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Handled: true}}

			progress := events.NewOutboundMessage(events.SourceSlack, "conversation", "progress")
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseProgress, Message: progress}}

			final := events.NewOutboundMessage(events.SourceSlack, "conversation", "final")

			final.Complete = true
			request.Response <- events.Response{Payload: &events.TextResponse{Kind: events.ResponseResult, Message: final}}
		})

		router := newRequestTextRouter(channels.Requests)
		router.output = func(context.Context, *events.OutboundMessage) error { return context.Canceled }
		router.abort = func(message *events.OutboundMessage) { aborted <- message.Text }
		_, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
		require.NoError(t, err)
		require.Equal(t, "progress", <-aborted)
		require.Equal(t, "final", <-aborted)

		cancel()
		synctest.Wait()
	})
}

func TestClockworkBuffersBroadcastBeforeBridges(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	message := events.NewOutboundMessage(events.SourceSystem, "conversation", "buffered")
	clockwork.dispatch(&events.Broadcast{Message: message, Delivery: message})
	require.Len(t, clockwork.pending, 1)
	require.Equal(t, "buffered", clockwork.pending[0].Message.Text)
}

func TestClockworkRegisterBridgeDuplicate(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	bridge := &clockworkTestBridge{received: make(chan *events.Broadcast, 1), status: events.BroadcastHandled}
	unregister, err := clockwork.registerBridge(events.BridgeSlack, bridge)
	require.NoError(t, err)
	_, err = clockwork.registerBridge(events.BridgeSlack, bridge)
	require.Error(t, err)
	unregister()
}

func TestRequestTextRouterRejectsUnexpectedPayload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(_ context.Context, request events.Request) {
			request.Response <- events.Response{Payload: clockworkTestPayload{value: "nope"}}
		})

		router := newRequestTextRouter(channels.Requests)
		_, err := router.SubmitThreadReply(t.Context(), events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil)
		require.ErrorContains(t, err, "unexpected response")
		cancel()
		synctest.Wait()
	})
}

func TestClockworkRunRejectsSecondStart(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- clockwork.run(ctx, func(context.Context, events.Request) {}) }()
	// wait until started
	for {
		clockwork.mu.Lock()
		started := clockwork.started
		clockwork.mu.Unlock()

		if started {
			break
		}
	}

	require.Error(t, clockwork.run(ctx, func(context.Context, events.Request) {}))
	cancel()
	require.NoError(t, <-errCh)
}

func TestDropBroadcastBridge(t *testing.T) {
	ack := (dropBroadcastBridge{}).HandleBroadcast(t.Context(), &events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "c", "x")})
	require.Equal(t, events.BroadcastDropped, ack.Status)
}

func TestDispatchMarksDeliveryWhenNoBridges(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "solo")
	clockwork.dispatch(&events.Broadcast{Message: delivery, Delivery: delivery})
	require.NoError(t, delivery.WaitDelivered(t.Context()))
}

func TestNewRequestTextRouterDefaults(t *testing.T) {
	router := newRequestTextRouter(make(chan events.Request))
	require.NoError(t, router.output(t.Context(), events.NewOutboundMessage(events.SourceSlack, "c", "x")))
	router.abort(events.NewOutboundMessage(events.SourceSlack, "c", "x"))
	_, err := router.root(t.Context(), &events.StartNewThreadRequest{})
	require.Error(t, err)
}

func TestRequestTextRouterContextCanceledOnSend(t *testing.T) {
	router := newRequestTextRouter(make(chan events.Request))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := router.SubmitThreadReply(ctx, events.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil)
	require.Error(t, err)
}

func TestCloseBridgesFailsPending(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "pending")
	clockwork.pending = []events.Broadcast{{Delivery: delivery}}
	clockwork.closeBridges()
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
}

func TestRegisteredBridgeEnqueueAfterCloseFailsBroadcast(t *testing.T) {
	bridge := newRegisteredBridge(events.BridgeSlack, &clockworkTestBridge{received: make(chan *events.Broadcast, 1), status: events.BroadcastHandled})
	bridge.close()

	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "late")
	relay := make(chan events.BroadcastReply, 1)
	bridge.enqueue(&events.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestRequestTextRouterHandlesChildThreadInteraction(t *testing.T) {
	router := newRequestTextRouter(make(chan events.Request))
	want := events.StartNewThreadRootResult{Target: events.TextConversationTarget{ChannelID: "C1", MessageID: "1.2", ThreadID: "1.2"}, URL: "https://slack.example/thread"}
	router.root = func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error) {
		return want, nil
	}
	root := make(chan events.StartNewThreadRootResult, 1)
	errCh := make(chan error, 1)
	handled, err := router.handleInteraction(t.Context(), events.StartNewThreadResponse{Request: &events.StartNewThreadRequest{}, Root: root, Err: errCh})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, want, <-root)

	router.root = func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error) {
		return events.StartNewThreadRootResult{}, context.Canceled
	}
	root = make(chan events.StartNewThreadRootResult, 1)
	errCh = make(chan error, 1)
	handled, err = router.handleInteraction(t.Context(), events.StartNewThreadResponse{Request: &events.StartNewThreadRequest{}, Root: root, Err: errCh})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, handled)
	require.ErrorIs(t, <-errCh, context.Canceled)

	handled, err = router.handleInteraction(t.Context(), &events.TextResponse{Kind: events.ResponseResult})
	require.NoError(t, err)
	require.False(t, handled)
}

func TestFailBroadcastMarksDeliveryAndRelay(t *testing.T) {
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")
	relay := make(chan events.BroadcastReply, 1)
	failBroadcast(&events.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestClockworkBroadcastsExcludeSenderAndAcknowledge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastDropped}
		failed := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastFailed}
		unregisterSlack, err := clockwork.registerBridge(events.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(events.BridgeExternalMCP, mcp)
		require.NoError(t, err)
		unregisterFailed, err := clockwork.registerBridge(events.BridgeID("failed"), failed)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(context.Context, events.Request) {})

		channels.Broadcasts <- events.Broadcast{Sender: events.BridgeSlack, Message: events.NewOutboundMessage(events.SourceSlack, "conversation", "reply")}

		synctest.Wait()

		select {
		case <-slack.received:
			t.Fatal("sender received its own broadcast")
		default:
		}

		mcpBroadcast := <-mcp.received
		failedBroadcast := <-failed.received

		synctest.Wait()

		acknowledgement := <-mcpBroadcast.Acknowledgement
		require.Equal(t, events.BroadcastDropped, acknowledgement.Status)
		require.NoError(t, acknowledgement.Err)
		require.Equal(t, events.BroadcastFailed, (<-failedBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		unregisterFailed()
		cancel()
		synctest.Wait()
	})
}

func TestDropBroadcastBridgeCompletesDelivery(t *testing.T) {
	message := events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")
	bridge := dropBroadcastBridge{}
	broadcast := &events.Broadcast{Message: message, Delivery: message}

	acknowledgement := bridge.HandleBroadcast(t.Context(), broadcast)
	require.Equal(t, events.BroadcastDropped, acknowledgement.Status)
}

func TestClockworkNoSenderBroadcastReachesAllBridges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastDropped}
		unregisterSlack, err := clockwork.registerBridge(events.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(events.BridgeExternalMCP, mcp)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(context.Context, events.Request) {})

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")}

		synctest.Wait()

		slackBroadcast := <-slack.received
		mcpBroadcast := <-mcp.received

		synctest.Wait()
		require.Equal(t, events.BroadcastHandled, (<-slackBroadcast.Acknowledgement).Status)
		require.Equal(t, events.BroadcastDropped, (<-mcpBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkSlowBridgeDoesNotBlockAnotherBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slow := &blockingClockworkTestBridge{received: make(chan *events.Broadcast, 1), release: make(chan struct{})}
		fast := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregisterSlow, err := clockwork.registerBridge(events.BridgeExternalMCP, slow)
		require.NoError(t, err)
		unregisterFast, err := clockwork.registerBridge(events.BridgeSlack, fast)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(context.Context, events.Request) {})

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "first")}

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "second")}

		synctest.Wait()

		first := <-fast.received
		second := <-fast.received

		require.Equal(t, "first", first.Message.Text)
		require.Equal(t, "second", second.Message.Text)

		close(slow.release)
		<-slow.received
		synctest.Wait()

		unregisterSlow()
		unregisterFast()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkReconnectReceivesOnlyLaterBroadcasts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		firstBridge := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregister, err := clockwork.registerBridge(events.BridgeSlack, firstBridge)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork, func(context.Context, events.Request) {})
		synctest.Wait()

		unregister()

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "missed")}

		synctest.Wait()

		select {
		case <-firstBridge.received:
			t.Fatal("unregistered bridge received a broadcast")
		default:
		}

		reconnected := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregister, err = clockwork.registerBridge(events.BridgeSlack, reconnected)
		require.NoError(t, err)

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "later")}

		synctest.Wait()

		got := <-reconnected.received
		require.Equal(t, "later", got.Message.Text)

		unregister()
		cancel()
		synctest.Wait()
	})
}

func TestDispatchClockworkRequestCoversTextOperations(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "main"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
	target := events.TextConversationTarget{ChannelID: "C123", ThreadID: "111.222", MessageID: "111.222"}

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, conversationID, "hello", true)
	response := make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextSubmitThreadReply, Target: target, Inbound: inbound},
		Response:  response,
	})
	got := <-response
	require.NoError(t, got.Err)
	assert.Equal(t, events.BridgeSlack, inbound.Bridge)
	assert.Equal(t, response, inbound.Response)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeExternalMCP,
		Operation: &events.TextRequest{Kind: events.RequestTextThreadAgent, Target: target},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)
	payload, ok := got.Payload.(*events.TextResponse)
	require.True(t, ok)
	assert.Equal(t, "main", payload.Agent)
	assert.True(t, payload.Handled)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: clockworkTestOperation{value: "unsupported"},
		Response:  response,
	})
	got = <-response
	require.Error(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextRegisterThread, Target: target, Agent: "planner"},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextInterruptThread, Target: target},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextInterruptConversation, ConversationID: conversationID},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextSwitchThreadAgent, Target: target, Agent: "planner"},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextRegisterCronThread, Target: target, Agent: "main"},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextReserveWorkflowTurn, Target: target},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)
	payload, ok = got.Payload.(*events.TextResponse)
	require.True(t, ok)

	if payload.Release != nil {
		payload.Release <- struct{}{}
	}

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeExternalMCP,
		Operation: &events.TextRequest{Kind: events.RequestTextSubmitExternalMCP, Agent: "main", ConversationID: conversationID, Inbound: events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, conversationID, "hi", true)},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: events.RequestTextStartThread, Agent: "main", Target: target, Inbound: events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, conversationID, "start", true)},
		Response:  response,
	})
	got = <-response
	require.NoError(t, got.Err)

	response = make(chan events.Response, 1)
	dispatchClockworkRequest(t.Context(), manager, events.Request{
		Sender:    events.BridgeSlack,
		Operation: &events.TextRequest{Kind: "nope"},
		Response:  response,
	})
	got = <-response
	require.Error(t, got.Err)
}
