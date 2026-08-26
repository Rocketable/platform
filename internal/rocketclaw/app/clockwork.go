package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"golang.org/x/sync/errgroup"
)

type clockworkBridge interface {
	HandleBroadcast(context.Context, *events.Broadcast) events.BroadcastAcknowledgement
}

type dropBroadcastBridge struct{}

func (dropBroadcastBridge) HandleBroadcast(_ context.Context, _ *events.Broadcast) events.BroadcastAcknowledgement {
	return events.BroadcastAcknowledgement{Status: events.BroadcastDropped}
}

type requestTextRouter struct {
	requests  chan<- events.Request
	output    func(context.Context, *events.OutboundMessage) error
	abort     func(*events.OutboundMessage)
	root      func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error)
	turnPhase func(events.TextConversationTarget) harnessbridge.ThreadTurnPhase
}

var _ harnessbridge.PrimaryTextRouter = (*requestTextRouter)(nil)

func newRequestTextRouter(requests chan<- events.Request) *requestTextRouter {
	return &requestTextRouter{
		requests: requests,
		output:   func(context.Context, *events.OutboundMessage) error { return nil },
		abort:    func(*events.OutboundMessage) {},
		root: func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error) {
			return events.StartNewThreadRootResult{}, errors.New("thread bridge is not ready")
		},
		turnPhase: func(events.TextConversationTarget) harnessbridge.ThreadTurnPhase {
			return harnessbridge.ThreadTurnUnclassified
		},
	}
}

func (r *requestTextRouter) StartThread(ctx context.Context, agent string, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextStartThread, Agent: agent, Target: target, Inbound: inbound})

	return err
}

func (r *requestTextRouter) StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextStartGoal, Agent: agent, Objective: objective, CheckScript: checkScript, MaxTurns: maxTurns, Target: target, Inbound: inbound})

	return err
}

func (r *requestTextRouter) StartWorkflowInThread(ctx context.Context, agent, name, args string, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextStartWorkflow, Agent: agent, Name: name, Args: args, Target: target, Inbound: inbound})

	return err
}

func (r *requestTextRouter) ReserveWorkflowTurn(target events.TextConversationTarget) (release func(), reserved bool, err error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextReserveWorkflowTurn, Target: target})
	if err != nil || !result.Reserved {
		return nil, false, err
	}

	return func() { result.Release <- struct{}{} }, true, nil
}

func (r *requestTextRouter) WorkflowDescriptions() ([]workflow.Description, error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextWorkflowDescriptions})

	return result.Descriptions, err
}

func (r *requestTextRouter) InterruptConversation(conversationID string) *events.InboundMessage {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextInterruptConversation, ConversationID: conversationID})
	if err != nil {
		return nil
	}

	return result.Inbound
}

func (r *requestTextRouter) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextInterruptThread, Target: target})

	return result.Inbound, err
}

func (r *requestTextRouter) RegisterThread(target events.TextConversationTarget, agent string) (bool, error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextRegisterThread, Agent: agent, Target: target})

	return result.Created, err
}

func (r *requestTextRouter) RegisterCronThread(ctx context.Context, target events.TextConversationTarget, agent string) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextRegisterCronThread, Agent: agent, Target: target})

	return err
}

func (r *requestTextRouter) ThreadAgent(target events.TextConversationTarget) (agent string, handled bool, err error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextThreadAgent, Target: target})

	return result.Agent, result.Handled, err
}

func (r *requestTextRouter) SwitchThreadAgent(target events.TextConversationTarget, agent string) (bool, error) {
	result, err := r.request(context.Background(), &events.TextRequest{Kind: events.RequestTextSwitchThreadAgent, Agent: agent, Target: target})

	return result.Handled, err
}

func (r *requestTextRouter) SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	result, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextSubmitThreadReply, Target: target, Inbound: inbound})

	return result.Handled, err
}

func (r *requestTextRouter) SubmitWhenActive(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage, activation harnessbridge.ActivationHook) (bool, error) {
	result, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextSubmitWhenActive, Target: target, Inbound: inbound, Activation: activation})

	return result.Handled, err
}

func (r *requestTextRouter) StashThreadQueueItem(ctx context.Context, target events.TextConversationTarget, item *harnessbridge.ThreadQueueItem) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextStashThreadQueue, Target: target, QueueItem: threadQueueRecord(item)})

	return err
}

func (r *requestTextRouter) ThreadQueueItems(ctx context.Context, target events.TextConversationTarget) ([]harnessbridge.ThreadQueueItem, error) {
	result, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextListThreadQueue, Target: target})

	return threadQueueItems(result.QueueItems), err
}

func (r *requestTextRouter) ReorderThreadQueue(ctx context.Context, target events.TextConversationTarget, ids []string) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextReorderThreadQueue, Target: target, QueueIDs: ids})

	return err
}

func (r *requestTextRouter) DeleteThreadQueueItem(ctx context.Context, target events.TextConversationTarget, id string) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextDeleteThreadQueueItem, Target: target, QueueItemID: id})

	return err
}

func (r *requestTextRouter) ScheduledMessages(ctx context.Context, target events.TextConversationTarget) (map[string]harnessbridge.ScheduledMessageState, error) {
	result, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextListScheduledMessages, Target: target})

	return scheduledMessageStates(result.ScheduledMessages), err
}

func (r *requestTextRouter) DeleteScheduledMessage(ctx context.Context, target events.TextConversationTarget, id string) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextDeleteScheduledMessage, Target: target, QueueItemID: id})

	return err
}

func (r *requestTextRouter) ResetScheduledMessages(ctx context.Context, target events.TextConversationTarget) error {
	_, err := r.request(ctx, &events.TextRequest{Kind: events.RequestTextResetScheduledMessages, Target: target})

	return err
}

func (r *requestTextRouter) TurnPhase(target events.TextConversationTarget) harnessbridge.ThreadTurnPhase {
	return r.turnPhase(target)
}

func threadQueueRecord(item *harnessbridge.ThreadQueueItem) events.ThreadQueueRecord {
	return events.ThreadQueueRecord{ID: item.ID, Message: item.Message, Principal: item.Principal, SlackChannel: item.SlackChannel, SlackTS: item.SlackTS, ParkAfter: item.ParkAfter, StashAt: item.StashAt, Position: item.Position}
}

func threadQueueItems(records []events.ThreadQueueRecord) []harnessbridge.ThreadQueueItem {
	items := make([]harnessbridge.ThreadQueueItem, len(records))
	for i := range records {
		items[i] = harnessbridge.ThreadQueueItem{ID: records[i].ID, Message: records[i].Message, Principal: records[i].Principal, SlackChannel: records[i].SlackChannel, SlackTS: records[i].SlackTS, ParkAfter: records[i].ParkAfter, StashAt: records[i].StashAt, Position: records[i].Position}
	}

	return items
}

func scheduledMessageStates(records map[string]events.ScheduledMessageRecord) map[string]harnessbridge.ScheduledMessageState {
	messages := make(map[string]harnessbridge.ScheduledMessageState, len(records))
	for id, record := range records {
		messages[id] = harnessbridge.ScheduledMessageState{ConversationID: record.ConversationID, Agent: record.Agent, Message: record.Message, DueAt: record.DueAt, Recurring: record.Recurring, Interval: record.Interval}
	}

	return messages
}

func threadQueueRecords(items []harnessbridge.ThreadQueueItem) []events.ThreadQueueRecord {
	records := make([]events.ThreadQueueRecord, len(items))
	for i := range items {
		records[i] = threadQueueRecord(&items[i])
	}

	return records
}

func scheduledMessageRecords(messages map[string]harnessbridge.ScheduledMessageState) map[string]events.ScheduledMessageRecord {
	records := make(map[string]events.ScheduledMessageRecord, len(messages))
	for id, message := range messages {
		records[id] = events.ScheduledMessageRecord{ID: id, ConversationID: message.ConversationID, Agent: message.Agent, Message: message.Message, DueAt: message.DueAt, Recurring: message.Recurring, Interval: message.Interval}
	}

	return records
}

func (r *requestTextRouter) request(ctx context.Context, operation *events.TextRequest) (events.TextResponse, error) {
	response := make(chan events.Response, 8)
	request := events.Request{Sender: events.BridgeSlack, Operation: operation, Response: response}

	select {
	case r.requests <- request:
	case <-ctx.Done():
		return events.TextResponse{}, fmt.Errorf("send text request: %w", ctx.Err())
	}

	for {
		select {
		case result := <-response:
			handled, err := r.handleInteraction(ctx, result.Payload)
			if err != nil {
				return events.TextResponse{}, err
			}

			if handled {
				continue
			}

			payload, ok := result.Payload.(*events.TextResponse)
			if !ok {
				return events.TextResponse{}, errors.New("text request returned an unexpected response")
			}

			if payload.Message != nil {
				if err := r.output(ctx, payload.Message); err != nil {
					if payload.Message.Complete {
						r.abort(payload.Message)
						payload.Message.MarkDelivered(err)

						return events.TextResponse{}, err
					}

					r.abort(payload.Message)

					continue
				}

				if payload.Message.Complete {
					payload.Message.MarkDelivered(nil)
				}

				continue
			}

			if operation.Inbound != nil && result.Err == nil {
				go r.consumeOutput(ctx, response)
			}

			return *payload, result.Err
		case <-ctx.Done():
			return events.TextResponse{}, fmt.Errorf("wait for text response: %w", ctx.Err())
		}
	}
}

func (r *requestTextRouter) consumeOutput(ctx context.Context, responses <-chan events.Response) {
	for {
		select {
		case result := <-responses:
			handled, err := r.handleInteraction(ctx, result.Payload)
			if err != nil {
				return
			}

			if handled {
				continue
			}

			payload, ok := result.Payload.(*events.TextResponse)
			if !ok || payload.Message == nil {
				return
			}

			if err := r.output(ctx, payload.Message); err != nil {
				if payload.Message.Complete {
					r.abort(payload.Message)
					payload.Message.MarkDelivered(err)

					return
				}

				r.abort(payload.Message)

				continue
			}

			if payload.Message.Complete {
				payload.Message.MarkDelivered(nil)

				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *requestTextRouter) handleInteraction(ctx context.Context, payload events.ResponsePayload) (bool, error) {
	switch interaction := payload.(type) {
	case events.StartNewThreadResponse:
		root, err := r.root(ctx, interaction.Request)
		if err != nil {
			interaction.Err <- err

			return true, err
		}

		interaction.Root <- root

		return true, nil
	default:
		return false, nil
	}
}

func dispatchTextRequest(ctx context.Context, manager *threadBridgeManager, operation *events.TextRequest) (*events.TextResponse, error) {
	result := &events.TextResponse{Kind: events.ResponseResult}

	var err error

	switch operation.Kind {
	case events.RequestTextStartThread:
		return result, manager.StartThread(ctx, operation.Agent, operation.Target, operation.Inbound)
	case events.RequestTextStartGoal:
		return result, manager.StartGoalInThread(ctx, operation.Agent, operation.Objective, operation.CheckScript, operation.MaxTurns, operation.Target, operation.Inbound)
	case events.RequestTextStartWorkflow:
		return result, manager.StartWorkflowInThread(ctx, operation.Agent, operation.Name, operation.Args, operation.Target, operation.Inbound)
	case events.RequestTextReserveWorkflowTurn:
		release, reserved, err := manager.ReserveWorkflowTurn(operation.Target)

		result.Reserved = reserved
		if err != nil || !reserved {
			return result, err
		}

		result.Release = make(chan struct{}, 1)

		go func() {
			select {
			case <-result.Release:
				release()
			case <-ctx.Done():
				release()
			}
		}()

		return result, nil
	case events.RequestTextWorkflowDescriptions:
		result.Descriptions, err = manager.WorkflowDescriptions()

		return result, err
	case events.RequestTextInterruptConversation:
		result.Inbound = manager.InterruptConversation(operation.ConversationID)

		return result, nil
	case events.RequestTextInterruptThread:
		result.Inbound, err = manager.InterruptThread(operation.Target)

		return result, err
	case events.RequestTextRegisterThread:
		result.Created, err = manager.RegisterThread(operation.Target, operation.Agent)

		return result, err
	case events.RequestTextRegisterCronThread:
		return result, manager.RegisterCronThread(ctx, operation.Target, operation.Agent)
	case events.RequestTextThreadAgent:
		result.Agent, result.Handled, err = manager.ThreadAgent(operation.Target)

		return result, err
	case events.RequestTextSwitchThreadAgent:
		result.Handled, err = manager.SwitchThreadAgent(operation.Target, operation.Agent)

		return result, err
	case events.RequestTextSubmitThreadReply:
		result.Handled, err = manager.SubmitThreadReply(ctx, operation.Target, operation.Inbound)

		return result, err
	case events.RequestTextSubmitWhenActive:
		result.Handled, err = manager.SubmitWhenActive(ctx, operation.Target, operation.Inbound, operation.Activation)

		return result, err
	case events.RequestTextStashThreadQueue:
		item := threadQueueItems([]events.ThreadQueueRecord{operation.QueueItem})[0]

		return result, manager.StashThreadQueueItem(ctx, operation.Target, &item)
	case events.RequestTextListThreadQueue:
		items, errList := manager.ThreadQueueItems(ctx, operation.Target)
		result.QueueItems = threadQueueRecords(items)

		return result, errList
	case events.RequestTextReorderThreadQueue:
		return result, manager.ReorderThreadQueue(ctx, operation.Target, operation.QueueIDs)
	case events.RequestTextDeleteThreadQueueItem:
		return result, manager.DeleteThreadQueueItem(ctx, operation.Target, operation.QueueItemID)
	case events.RequestTextListScheduledMessages:
		messages, errList := manager.ScheduledMessages(ctx, operation.Target)
		result.ScheduledMessages = scheduledMessageRecords(messages)

		return result, errList
	case events.RequestTextDeleteScheduledMessage:
		return result, manager.DeleteScheduledMessage(ctx, operation.Target, operation.QueueItemID)
	case events.RequestTextResetScheduledMessages:
		return result, manager.ResetScheduledMessages(ctx, operation.Target)
	case events.RequestTextSubmitExternalMCP:
		return result, manager.SubmitExternalMCP(ctx, operation.Agent, operation.ConversationID, operation.Inbound, harnessbridge.NoopActivationHook)
	default:
		return result, fmt.Errorf("unsupported text request kind %q", operation.Kind)
	}
}

func dispatchClockworkRequest(ctx context.Context, manager *threadBridgeManager, request events.Request) {
	operation, ok := request.Operation.(*events.TextRequest)
	if !ok {
		sendClockworkResponse(ctx, request.Response, events.Response{Err: fmt.Errorf("unsupported request operation %T", request.Operation)})

		return
	}

	if operation.Inbound != nil {
		operation.Inbound.Bridge = request.Sender
		if request.Sender == events.BridgeSlack {
			operation.Inbound.Response = request.Response
		}
	}

	payload, err := dispatchTextRequest(ctx, manager, operation)
	sendClockworkResponse(ctx, request.Response, events.Response{Payload: payload, Err: err})
}

func sendClockworkResponse(ctx context.Context, response chan events.Response, message events.Response) {
	select {
	case response <- message:
	case <-ctx.Done():
	}
}

type clockwork struct {
	channels events.Channels

	mu             sync.Mutex
	bridges        map[events.BridgeID]*registeredBridge
	started        bool
	done           chan struct{}
	registerCh     chan bridgeRegistrationRequest
	pending        []events.Broadcast
	pendingEnabled bool
}

type bridgeRegistrationRequest struct {
	bridge *registeredBridge
	ready  chan error
}

type registeredBridge struct {
	id      events.BridgeID
	handler clockworkBridge

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*events.Broadcast
	closed bool
}

func newClockwork(channels events.Channels) *clockwork {
	return &clockwork{
		channels:   channels,
		bridges:    make(map[events.BridgeID]*registeredBridge),
		done:       make(chan struct{}),
		registerCh: make(chan bridgeRegistrationRequest),
	}
}

func newRegisteredBridge(id events.BridgeID, handler clockworkBridge) *registeredBridge {
	bridge := &registeredBridge{id: id, handler: handler}
	bridge.cond = sync.NewCond(&bridge.mu)

	return bridge
}

func (c *clockwork) registerBridge(id events.BridgeID, handler clockworkBridge) (func(), error) {
	bridge := newRegisteredBridge(id, handler)

	c.mu.Lock()
	if _, exists := c.bridges[id]; exists {
		c.mu.Unlock()
		return nil, errors.New("bridge already registered")
	}

	c.bridges[id] = bridge
	started := c.started
	done := c.done
	c.mu.Unlock()

	if started {
		request := bridgeRegistrationRequest{bridge: bridge, ready: make(chan error, 1)}
		select {
		case c.registerCh <- request:
			if err := <-request.ready; err != nil {
				c.removeBridge(bridge)
				bridge.close()

				return nil, err
			}
		case <-done:
			c.removeBridge(bridge)
			bridge.close()

			return nil, context.Canceled
		}
	}

	return func() { c.unregisterBridge(bridge) }, nil
}

func (c *clockwork) unregisterBridge(bridge *registeredBridge) {
	c.removeBridge(bridge)
	bridge.close()
}

func (c *clockwork) removeBridge(bridge *registeredBridge) {
	c.mu.Lock()
	if c.bridges[bridge.id] == bridge {
		delete(c.bridges, bridge.id)
	}
	c.mu.Unlock()
}

func (c *clockwork) run(ctx context.Context, handleRequest func(context.Context, events.Request)) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()

		return errors.New("clockwork already running")
	}

	c.started = true
	c.pendingEnabled = len(c.bridges) == 0

	bridgeCount := len(c.bridges)

	bridges := make([]*registeredBridge, 0, bridgeCount)
	for _, bridge := range c.bridges {
		bridges = append(bridges, bridge)
	}
	c.mu.Unlock()

	group, groupCtx := errgroup.WithContext(ctx)
	startBridge := func(bridge *registeredBridge) {
		group.Go(func() error {
			bridge.run(groupCtx)

			return nil
		})
	}

	for _, bridge := range bridges {
		startBridge(bridge)
	}

	group.Go(func() error {
		for {
			select {
			case <-groupCtx.Done():
				c.closeBridges()

				return nil
			case request := <-c.channels.Requests:
				group.Go(func() error {
					handleRequest(groupCtx, request)

					return nil
				})
			case broadcast := <-c.channels.Broadcasts:
				c.dispatch(&broadcast)
			case registration := <-c.registerCh:
				startBridge(registration.bridge)
				c.mu.Lock()
				pending := c.pending
				c.pending = nil
				c.pendingEnabled = false
				c.mu.Unlock()

				for i := range pending {
					c.dispatch(&pending[i])
				}

				registration.ready <- nil
			}
		}
	})

	err := group.Wait()

	c.closeBridges()
	close(c.done)

	return errors.Join(err)
}

func (c *clockwork) dispatch(broadcast *events.Broadcast) {
	c.mu.Lock()

	bridgeCount := len(c.bridges)
	pendingEnabled := c.pendingEnabled

	bridges := make([]*registeredBridge, 0, bridgeCount)
	for id, bridge := range c.bridges {
		if broadcast.Sender == "" || id != broadcast.Sender {
			bridges = append(bridges, bridge)
		}
	}
	c.mu.Unlock()

	if len(bridges) == 0 && bridgeCount == 0 && pendingEnabled {
		c.mu.Lock()
		c.pending = append(c.pending, *broadcast)
		c.mu.Unlock()

		return
	}

	if len(bridges) == 0 && broadcast.Delivery != nil && broadcast.Message.Response == nil {
		broadcast.Delivery.MarkDelivered(nil)
	}

	for _, bridge := range bridges {
		clone := broadcast.Clone()
		bridge.enqueue(&clone)
	}
}

func (c *clockwork) closeBridges() {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil

	bridges := make([]*registeredBridge, 0, len(c.bridges))
	for _, bridge := range c.bridges {
		bridges = append(bridges, bridge)
	}
	c.mu.Unlock()

	for _, bridge := range bridges {
		bridge.close()
	}

	for i := range pending {
		failBroadcast(&pending[i])
	}
}

func (b *registeredBridge) enqueue(broadcast *events.Broadcast) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		failBroadcast(broadcast)

		return
	}

	b.queue = append(b.queue, broadcast)
	b.cond.Signal()
	b.mu.Unlock()
}

func (b *registeredBridge) close() {
	b.mu.Lock()

	b.closed = true
	for _, broadcast := range b.queue {
		failBroadcast(broadcast)
	}

	b.queue = nil
	b.cond.Broadcast()
	b.mu.Unlock()
}

func failBroadcast(broadcast *events.Broadcast) {
	if broadcast.Delivery != nil {
		broadcast.Delivery.MarkDelivered(context.Canceled)
	}

	if broadcast.RelayResponse != nil {
		broadcast.RelayResponse <- events.BroadcastReply{Err: context.Canceled}
	}
}

func (b *registeredBridge) run(ctx context.Context) {
	stopWake := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer stopWake()

	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed && ctx.Err() == nil {
			b.cond.Wait()
		}

		if b.closed || ctx.Err() != nil {
			b.queue = nil
			b.mu.Unlock()

			return
		}

		broadcast := b.queue[0]
		clear(b.queue[:1])
		b.queue = b.queue[1:]
		b.mu.Unlock()

		acknowledgement := b.handler.HandleBroadcast(ctx, broadcast)
		broadcast.Acknowledgement <- acknowledgement
	}
}
