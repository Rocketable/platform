package workflow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/google/jsonschema-go/jsonschema"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"
)

const (
	localContext = "workflow context"
	localPhase   = "workflow phase"
)

// Worker describes an isolated workflow agent.
type Worker struct {
	Name, Instructions, Model string
	Tools                     []string
}

// RunRequest identifies one foreground workflow invocation.
type RunRequest struct {
	RunID, Args string
	Definition  *Definition
}

// AgentRequest describes one isolated agent call.
type AgentRequest struct {
	Worker Worker
	Prompt string
	Schema map[string]any
}

// AgentRunner runs one isolated agent call.
type AgentRunner interface {
	Run(context.Context, *AgentRequest) (json.RawMessage, error)
}

// Result is the rendered workflow result.
type Result struct {
	Text   string
	Silent bool
	Phases []protocol.PhaseUpdate
}

type workerValue Worker

func (w *workerValue) String() string      { return fmt.Sprintf("<worker %s>", w.Name) }
func (*workerValue) Type() string          { return "worker" }
func (*workerValue) Freeze()               {}
func (*workerValue) Truth() starlark.Bool  { return true }
func (*workerValue) Hash() (uint32, error) { return 0, errors.New("worker is unhashable") }

type engine struct {
	agent  AgentRunner
	cancel context.CancelCauseFunc
	runID  string
	strict bool

	mu                sync.Mutex
	phases            map[string]*protocol.PhaseUpdate
	phaseSequence     int
	active            map[*starlark.Thread]uint64
	callbacks, agents int
	remaining         uint64
}

// Run executes a compiled workflow in the foreground.
func Run(ctx context.Context, definition *Definition, request RunRequest, agent AgentRunner) (result Result, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	e := &engine{agent: agent, cancel: cancel, runID: request.RunID, phases: make(map[string]*protocol.PhaseUpdate), phaseSequence: len(definition.Phases), strict: len(definition.Phases) > 0, active: make(map[*starlark.Thread]uint64), remaining: 10_000_000}
	defer func() {
		e.finishPhase("run", err)

		// Fan-out callbacks have joined before Run returns, so finalization owns phase state.
		for _, name := range definition.Phases {
			state := e.phases[name]
			if state.Status == protocol.PhasePending {
				state.Status = protocol.PhaseSkipped
			}
		}

		result.Phases = make([]protocol.PhaseUpdate, 0, len(e.phases))
		for _, state := range e.phases {
			result.Phases = append(result.Phases, *state)
		}

		slices.SortFunc(result.Phases, func(a, b protocol.PhaseUpdate) int { return cmp.Compare(a.PhaseID, b.PhaseID) })
	}()

	for i, name := range definition.Phases {
		e.phases[name] = &protocol.PhaseUpdate{PhaseID: fmt.Sprintf("%s/phase/%06d/%s", request.RunID, i, name), Name: name, Status: protocol.PhasePending}
	}

	thread, stop := e.thread(ctx, "workflow "+definition.Name, "", false)
	defer stop()

	if errContext := context.Cause(ctx); errContext != nil {
		return Result{}, fmt.Errorf("initialize workflow: %w", errContext)
	}

	globals, errInit := definition.program.Init(thread, e.builtins())
	if errInit != nil {
		if errContext := context.Cause(ctx); errContext != nil {
			return Result{}, fmt.Errorf("initialize workflow: %w", errContext)
		}

		return Result{}, fmt.Errorf("initialize workflow: %w", workflowEvalError(errInit))
	}

	globals.Freeze()

	value, errCall := starlark.Call(thread, globals["main"], starlark.Tuple{starlark.String(request.Args)}, nil)
	if errCall != nil {
		errCall = workflowEvalError(errCall)
		if errContext := context.Cause(ctx); errContext != nil {
			return Result{}, fmt.Errorf("call workflow: %w", errors.Join(errCall, errContext))
		}

		return Result{}, fmt.Errorf("call workflow: %w", errCall)
	}

	encoded, errEncode := e.encode(thread, value)
	if errEncode != nil {
		return Result{}, fmt.Errorf("encode workflow result: %w", errEncode)
	}

	if text, ok := value.(starlark.String); ok {
		return Result{Text: string(text), Silent: text == ""}, nil
	}

	if value == starlark.None {
		return Result{Silent: true}, nil
	}

	return Result{Text: string(encoded)}, nil
}

func workflowEvalError(err error) error {
	errEval, ok := errors.AsType[*starlark.EvalError](err)
	if !ok {
		return err
	}

	for {
		errNested, ok := errors.AsType[*starlark.EvalError](errEval.Unwrap())
		if !ok {
			break
		}

		errEval = errNested
	}

	for _, v := range slices.Backward(errEval.CallStack) {
		frame := v
		if frame.Pos.Filename() != "<builtin>" {
			return fmt.Errorf("%s in %s: %w", frame.Pos, frame.Name, err)
		}
	}

	return err
}

func (e *engine) thread(ctx context.Context, name, phase string, fanout bool) (thread *starlark.Thread, stop func()) {
	thread = &starlark.Thread{Name: name, OnMaxSteps: e.maxSteps, Print: func(*starlark.Thread, string) {}}
	thread.SetLocal(localContext, ctx)
	thread.SetLocal(localPhase, phase)
	thread.SetLocal("workflow fanout", fanout)
	e.mu.Lock()
	reserved := min(uint64(1_000), e.remaining)
	e.remaining -= reserved
	e.active[thread] = reserved
	e.mu.Unlock()
	thread.SetMaxExecutionSteps(reserved)

	if reserved == 0 {
		thread.Cancel("workflow step limit exceeded")
	}

	stopCancel := context.AfterFunc(ctx, func() { thread.Cancel(context.Cause(ctx).Error()) })

	return thread, func() {
		stopCancel()
		e.mu.Lock()
		e.remaining += e.active[thread] - min(thread.ExecutionSteps(), e.active[thread])
		delete(e.active, thread)
		e.mu.Unlock()
	}
}

func (e *engine) maxSteps(thread *starlark.Thread) {
	e.mu.Lock()
	if e.remaining == 0 {
		for current := range e.active {
			current.Cancel("workflow step limit exceeded")
		}
		e.mu.Unlock()

		return
	}

	reserved := min(uint64(1_000), e.remaining)
	e.remaining -= reserved
	e.active[thread] += reserved
	thread.SetMaxExecutionSteps(e.active[thread])
	e.mu.Unlock()
}

func (e *engine) builtins() starlark.StringDict {
	worker := func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var name, instructions, model string

		tools := (*starlark.List)(nil)
		if err := starlark.UnpackArgs("worker", args, kwargs, "name", &name, "instructions", &instructions, "model??", &model, "tools??", &tools); err != nil {
			return nil, fmt.Errorf("worker arguments: %w", err)
		}

		w := &workerValue{Name: name, Instructions: instructions, Model: model}

		if tools != nil {
			w.Tools = []string{}

			for i := range tools.Len() {
				w.Tools = append(w.Tools, string(tools.Index(i).(starlark.String)))
			}
		}

		return w, nil
	}

	phase := func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var name string

		fn := starlark.Callable(nil)
		if err := starlark.UnpackArgs("phase", args, kwargs, "name", &name, "fn", &fn); err != nil {
			return nil, fmt.Errorf("phase arguments: %w", err)
		}

		if thread.Local(localPhase).(string) != "" {
			return nil, errors.New("nested phases are not allowed")
		}

		e.mu.Lock()

		state := e.phases[name]
		if state == nil && e.strict {
			e.mu.Unlock()
			return nil, fmt.Errorf("phase %q is not declared", name)
		}

		if state == nil {
			if len(e.phases) >= 100 {
				e.mu.Unlock()
				return nil, errors.New("workflow may have at most 100 phases")
			}

			state = &protocol.PhaseUpdate{PhaseID: fmt.Sprintf("%s/phase/%06d/%s", e.runID, e.phaseSequence, name), Name: name, Status: protocol.PhasePending}
			e.phaseSequence++
			e.phases[name] = state
		}

		if state.Status != protocol.PhasePending {
			e.mu.Unlock()
			return nil, fmt.Errorf("phase %q may execute only once", name)
		}

		state.Status = protocol.PhaseInProgress
		e.mu.Unlock()

		thread.SetLocal(localPhase, name)
		value, errCall := starlark.Call(thread, fn, nil, nil)
		thread.SetLocal(localPhase, "")
		e.finishPhase(name, errCall)

		if errCall != nil {
			return value, fmt.Errorf("call workflow phase %q: %w", name, errCall)
		}

		return value, nil
	}

	var fanout func(*starlark.Thread, []starlark.Value, func(*starlark.Thread, starlark.Value) (starlark.Value, error)) (starlark.Value, error)

	parallel := func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var callables starlark.Value
		if err := starlark.UnpackArgs("parallel", args, kwargs, "callables", &callables); err != nil {
			return nil, fmt.Errorf("parallel arguments: %w", err)
		}

		var callbacks []starlark.Value

		switch values := callables.(type) {
		case *starlark.List:
			callbacks = slices.Collect(values.Elements())
		case starlark.Tuple:
			callbacks = slices.Clone(values)
		default:
			return nil, errors.New("parallel callables must be a list or tuple")
		}

		return fanout(thread, callbacks, func(child *starlark.Thread, callback starlark.Value) (starlark.Value, error) {
			return starlark.Call(child, callback, nil, nil)
		})
	}

	fanout = func(thread *starlark.Thread, values []starlark.Value, call func(*starlark.Thread, starlark.Value) (starlark.Value, error)) (starlark.Value, error) {
		if thread.Local("workflow fanout").(bool) {
			return nil, errors.New("nested fan-out is not allowed")
		}

		e.mu.Lock()

		e.callbacks += len(values)
		if e.callbacks > 1_000 {
			e.mu.Unlock()
			return nil, errors.New("callback limit exceeded")
		}
		e.mu.Unlock()

		for _, value := range values {
			value.Freeze()
		}

		results := make(starlark.Tuple, len(values))
		ctx := thread.Local(localContext).(context.Context)
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(16)

		for i, value := range values {
			group.Go(func() error {
				child, stop := e.thread(groupCtx, fmt.Sprintf("workflow callback %d", i), thread.Local(localPhase).(string), true)
				defer stop()

				result, errCall := call(child, value)
				if errCall != nil {
					return fmt.Errorf("call workflow callback: %w", errCall)
				}

				results[i] = result

				return nil
			})
		}

		if err := group.Wait(); err != nil {
			return nil, fmt.Errorf("run workflow callbacks: %w", err)
		}

		return results, nil
	}

	return starlark.StringDict{
		"worker":   starlark.NewBuiltin("worker", worker),
		"agent":    e.agentBuiltin(),
		"phase":    starlark.NewBuiltin("phase", phase),
		"parallel": starlark.NewBuiltin("parallel", parallel),
		"pipeline": e.pipelineBuiltin(fanout),
	}
}

func (e *engine) pipelineBuiltin(fanout func(*starlark.Thread, []starlark.Value, func(*starlark.Thread, starlark.Value) (starlark.Value, error)) (starlark.Value, error)) *starlark.Builtin {
	return starlark.NewBuiltin("pipeline", func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var items starlark.Iterable

		fn := starlark.Callable(nil)
		if err := starlark.UnpackArgs("pipeline", args, kwargs, "items", &items, "fn", &fn); err != nil {
			return nil, fmt.Errorf("pipeline arguments: %w", err)
		}

		fn.Freeze()

		ctx := thread.Local(localContext).(context.Context)

		values, err := pipelineItems(ctx, items)
		if err != nil {
			return nil, err
		}

		return fanout(thread, values, func(child *starlark.Thread, value starlark.Value) (starlark.Value, error) {
			return starlark.Call(child, fn, starlark.Tuple{value}, nil)
		})
	})
}

func pipelineItems(ctx context.Context, items starlark.Iterable) ([]starlark.Value, error) {
	capacity := 1_000

	if sequence, ok := items.(starlark.Sequence); ok {
		if sequence.Len() > 1_000 {
			return nil, errors.New("callback limit exceeded")
		}

		capacity = sequence.Len()
	}

	values := make([]starlark.Value, 0, capacity)

	iterator := items.Iterate()
	defer iterator.Done()

	for {
		if err := context.Cause(ctx); err != nil {
			return nil, fmt.Errorf("collect pipeline items: %w", err)
		}

		var value starlark.Value
		if !iterator.Next(&value) {
			return values, nil
		}

		if len(values) == 1_000 {
			return nil, errors.New("callback limit exceeded")
		}

		values = append(values, value)
	}
}

func (e *engine) agentBuiltin() *starlark.Builtin {
	return starlark.NewBuiltin("agent", func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var prompt, label string

		worker := (*workerValue)(nil)

		schema := starlark.Value(starlark.None)

		var resolved *jsonschema.Resolved

		if err := starlark.UnpackArgs("agent", args, kwargs, "prompt", &prompt, "worker??", &worker, "label?", &label, "schema?", &schema); err != nil {
			return nil, fmt.Errorf("agent arguments: %w", err)
		}

		e.mu.Lock()

		e.agents++
		if e.agents > 1_000 {
			e.mu.Unlock()
			return nil, errors.New("agent limit exceeded")
		}
		e.mu.Unlock()

		phase := thread.Local(localPhase).(string)
		if phase == "" {
			phase = "run"
		}

		request := AgentRequest{Prompt: prompt}
		if worker != nil {
			request.Worker = Worker(*worker)
			request.Worker.Tools = slices.Clone(worker.Tools)
		}

		if schema != starlark.None {
			encoded, errSchema := e.encode(thread, schema)
			if errSchema != nil {
				return nil, fmt.Errorf("agent schema: %w", errSchema)
			}

			if err := json.Unmarshal(encoded, &request.Schema); err != nil {
				return nil, fmt.Errorf("agent schema: %w", err)
			}

			var definition jsonschema.Schema
			if err := json.Unmarshal(encoded, &definition); err != nil {
				return nil, fmt.Errorf("agent schema: %w", err)
			}

			resolved, errSchema = definition.Resolve(nil)
			if errSchema != nil {
				return nil, fmt.Errorf("agent schema: %w", errSchema)
			}
		}

		ctx := thread.Local(localContext).(context.Context)

		if err := e.phaseCount(phase, 1, 1, 0); err != nil {
			return nil, err
		}

		raw, errAgent := e.agent.Run(ctx, &request)
		if errAgent != nil {
			e.cancel(errAgent)

			return nil, fmt.Errorf("run workflow agent: %w", errAgent)
		}

		var instance any

		errResult := json.Unmarshal(raw, &instance)
		if errResult != nil {
			errResult = fmt.Errorf("decode agent result: %w", errResult)
		}

		if errResult == nil && resolved != nil {
			if errValidate := resolved.Validate(instance); errValidate != nil {
				errResult = fmt.Errorf("validate agent result: %w", errValidate)
			}
		}

		var decoded starlark.Value
		if errResult == nil {
			decoded, errResult = starlark.Call(thread, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(raw)}, nil)
			if errResult != nil {
				errResult = fmt.Errorf("decode agent result: %w", errResult)
			}
		}

		if errResult != nil {
			e.cancel(errResult)

			return nil, errResult
		}

		if err := e.phaseCount(phase, 0, -1, 1); err != nil {
			return nil, err
		}

		if schema == starlark.None {
			text, _ := starlark.AsString(decoded)
			return starlark.String(text), nil
		}

		return decoded, nil
	})
}

func (e *engine) phaseCount(name string, scheduled, running, complete int) error {
	e.mu.Lock()

	state := e.phases[name]
	if state == nil {
		if len(e.phases) >= 100 {
			e.mu.Unlock()
			return errors.New("workflow may have at most 100 phases")
		}

		state = &protocol.PhaseUpdate{PhaseID: fmt.Sprintf("%s/phase/%06d/%s", e.runID, e.phaseSequence, name), Name: name, Status: protocol.PhaseInProgress}
		e.phaseSequence++
		e.phases[name] = state
	}

	if state.Status == protocol.PhasePending {
		state.Status = protocol.PhaseInProgress
	}

	state.Scheduled += scheduled
	state.Running += running
	state.Complete += complete
	e.mu.Unlock()

	return nil
}

func (e *engine) finishPhase(name string, errRun error) {
	e.mu.Lock()

	state := e.phases[name]
	if state == nil || state.Status != protocol.PhaseInProgress {
		e.mu.Unlock()
		return
	}

	state.Running = 0

	state.Status = protocol.PhaseComplete
	if errRun != nil {
		state.Status = protocol.PhaseError
	}

	e.mu.Unlock()
}

func (e *engine) encode(thread *starlark.Thread, value starlark.Value) ([]byte, error) {
	encoded, err := starlark.Call(thread, starjson.Module.Members["encode"], starlark.Tuple{value}, nil)
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}

	if containsSet(value) {
		return nil, errors.New("set is not JSON-compatible")
	}

	return []byte(encoded.(starlark.String)), nil
}

func containsSet(value starlark.Value) bool {
	if _, ok := value.(*starlark.Set); ok {
		return true
	}

	var values []starlark.Value

	switch value := value.(type) {
	case *starlark.List:
		for i := range value.Len() {
			values = append(values, value.Index(i))
		}
	case starlark.Tuple:
		values = value
	case *starlark.Dict:
		for _, item := range value.Items() {
			values = append(values, item[1])
		}
	}

	return slices.ContainsFunc(values, containsSet)
}
