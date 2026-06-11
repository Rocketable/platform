// Command looper runs RocketCode toward a non-interactive goal.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/sync/errgroup"
)

const defaultAgent = "main"

type options struct {
	goal              string
	script            string
	maxLoops          int
	scriptOutputLimit int64
}

type goalClaim struct {
	Summary  string `json:"summary"`
	Evidence string `json:"evidence"`
}

type criticVerdict struct {
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback"`
}

type eventWriter struct {
	w io.Writer
}

type jsonlEvent struct {
	Type      string                         `json:"type"`
	Loop      int                            `json:"loop,omitempty"`
	Role      string                         `json:"role,omitempty"`
	Kind      string                         `json:"kind,omitempty"`
	Text      string                         `json:"text,omitempty"`
	Tool      *rocketcode.ToolDiagnostic     `json:"tool,omitempty"`
	Subagent  *rocketcode.SubagentDiagnostic `json:"subagent,omitempty"`
	Provider  *rocketcode.ProviderDiagnostic `json:"provider,omitempty"`
	Goal      *goalClaim                     `json:"goal,omitempty"`
	Verdict   *criticVerdict                 `json:"verdict,omitempty"`
	Script    *scriptResult                  `json:"script,omitempty"`
	Succeeded bool                           `json:"succeeded,omitempty"`
	Error     string                         `json:"error,omitempty"`
}

type claimRecorder = recorder[goalClaim]

type verdictRecorder = recorder[criticVerdict]

type recorder[T any] struct {
	mu    sync.Mutex
	value *T
}

type memorySession struct {
	mu      sync.Mutex
	entries []rocketcode.SessionEntry
}

type scriptResult struct {
	Command  string `json:"command,omitempty"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type scriptRunner func(context.Context, string, string, int64) (scriptResult, error)

type runtimeDeps struct {
	mainLooper   rocketcode.Looper
	criticLooper rocketcode.Looper
	root         *os.Root
	cwd          string
	interrupts   <-chan os.Signal
	runScript    scriptRunner
	claims       *claimRecorder
	verdicts     *verdictRecorder
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opt, err := parseOptions(args, stdin)
	if err != nil {
		return err
	}

	deps, cleanup, err := newRuntimeDeps(stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	return runAutonomousLoop(context.Background(), opt, &deps, &eventWriter{w: stdout})
}

func parseOptions(args []string, stdin io.Reader) (options, error) {
	flags := flag.NewFlagSet("looper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var opt options
	flags.StringVar(&opt.script, "script", "", "shell command that returns 0 when the goal is achieved")
	flags.IntVar(&opt.maxLoops, "max-loops", 0, "maximum main-agent loops; 0 means unlimited")
	flags.Int64Var(&opt.scriptOutputLimit, "script-output-limit", 0, "maximum bytes kept from each script output stream; 0 means unlimited")

	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("parse flags: %w", err)
	}

	if opt.maxLoops < 0 {
		return options{}, errors.New("--max-loops must be non-negative")
	}

	if opt.scriptOutputLimit < 0 {
		return options{}, errors.New("--script-output-limit must be non-negative")
	}

	positional := strings.TrimSpace(strings.Join(flags.Args(), " "))

	stdinText, err := stdinGoal(stdin)
	if err != nil {
		return options{}, err
	}

	if positional != "" && stdinText != "" {
		return options{}, errors.New("provide goal either as positional arguments or stdin, not both")
	}

	opt.goal = positional
	if opt.goal == "" {
		opt.goal = stdinText
	}

	if opt.goal == "" {
		return options{}, errors.New("goal is required as positional arguments or stdin")
	}

	return opt, nil
}

func stdinGoal(stdin io.Reader) (string, error) {
	file, ok := stdin.(*os.File)
	if ok {
		info, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("stat stdin: %w", err)
		}

		if info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func newRuntimeDeps(diagnostics io.Writer) (runtimeDeps, func(), error) {
	config, err := rocketcode.StandaloneConfigFromEnv()
	if err != nil {
		return runtimeDeps{}, nil, rocketcode.OperationError{Operation: rocketcode.OperationLoadConfig, Err: err}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return runtimeDeps{}, nil, fmt.Errorf("get current directory: %w", err)
	}

	root, err := os.OpenRoot(cwd)
	if err != nil {
		return runtimeDeps{}, nil, fmt.Errorf("open workspace root: %w", err)
	}

	cleanup := func() { _ = root.Close() }

	if err := root.MkdirAll(config.ShellOutputDir, 0o755); err != nil {
		cleanup()
		return runtimeDeps{}, nil, fmt.Errorf("create shell output dir: %w", err)
	}

	config.ShellOutputDir = filepath.Join(cwd, config.ShellOutputDir)

	agents, skills, cleanupDefinitions, err := rocketcode.LoadWorkspaceDefinitions(root)
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, rocketcode.OperationError{Operation: rocketcode.OperationLoadWorkspaceDefinitions, Err: err}
	}

	oldCleanup := cleanup
	cleanup = func() { cleanupDefinitions(); oldCleanup() }
	providers := rocketcode.StandaloneProvidersFromEnv()

	claims := &claimRecorder{}
	verdicts := &verdictRecorder{}

	mainConfig := config
	mainConfig.CustomTools = append(mainConfig.CustomTools, newGoalTool(claims))
	mainAgents := allowInternalTool(agents, "goal_achieved")

	mainLooper, err := rocketcode.NewWithProviders(providers, &mainConfig, root, mainAgents, skills, defaultAgent, diagnostics)
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, fmt.Errorf("initialize main rocketcode: %w", err)
	}

	criticConfig := config
	criticConfig.CustomTools = append(criticConfig.CustomTools, newCriticTool(verdicts))
	criticAgents := allowInternalTool(agents, "critic_verdict")

	criticLooper, err := rocketcode.NewWithProviders(providers, &criticConfig, root, criticAgents, skills, defaultAgent, diagnostics)
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, fmt.Errorf("initialize critic rocketcode: %w", err)
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)

	oldCleanup = cleanup
	cleanup = func() {
		signal.Stop(interrupts)
		oldCleanup()
	}

	return runtimeDeps{mainLooper: mainLooper, criticLooper: criticLooper, root: root, cwd: cwd, interrupts: interrupts, runScript: runShellScript, claims: claims, verdicts: verdicts}, cleanup, nil
}

func runAutonomousLoop(ctx context.Context, opt options, deps *runtimeDeps, events *eventWriter) error {
	mainSession := &memorySession{}
	mainSession.add(developerSessionEntry(mainInstructions(opt.goal)))

	prompt, err := promptInput(opt.goal, deps.root, deps.cwd)
	if err != nil {
		return err
	}

	for loop := 1; ; loop++ {
		if opt.maxLoops > 0 && loop > opt.maxLoops {
			_ = events.write(loopResultEvent(loop-1, false, "max loops exhausted"))
			return errors.New("max loops exhausted")
		}

		deps.claims.clear()
		deps.verdicts.clear()

		if err := runTurn(ctx, deps.mainLooper, mainSession, prompt, deps.interrupts, events, loop, "main"); err != nil {
			_ = events.write(loopResultEvent(loop, false, err.Error()))
			return err
		}

		claim := deps.claims.latest()
		if claim == nil {
			prompt = rocketcode.PromptInput{Role: rocketcode.PromptInputRoleDeveloper, Text: "You did not call goal_achieved. Continue working on the original goal, and call goal_achieved only when it is fully complete with concrete evidence."}
			continue
		}

		if err := events.write(&jsonlEvent{Type: "goal_achieved", Loop: loop, Goal: claim}); err != nil {
			return err
		}

		verdict, criticText, err := runCritic(ctx, deps, opt.goal, *claim, events, loop)
		if err != nil {
			_ = events.write(loopResultEvent(loop, false, err.Error()))
			return err
		}

		if verdict == nil || !verdict.Approved {
			feedback := criticText
			if verdict != nil {
				feedback = "critic_verdict rejected the completion claim. approved=false\nfeedback:\n" + verdict.Feedback
			} else if feedback == "" {
				feedback = "The critic did not call critic_verdict. Continue working on the original goal and provide stronger evidence before calling goal_achieved again."
			}

			prompt = rocketcode.PromptInput{Role: rocketcode.PromptInputRoleDeveloper, Text: feedback}

			continue
		}

		if opt.script == "" {
			return events.write(loopResultEvent(loop, true, ""))
		}

		result, err := deps.runScript(ctx, deps.cwd, opt.script, opt.scriptOutputLimit)
		if err != nil {
			_ = events.write(loopResultEvent(loop, false, err.Error()))
			return err
		}

		if err := events.write(&jsonlEvent{Type: "script_result", Loop: loop, Script: &result, Succeeded: result.ExitCode == 0}); err != nil {
			return err
		}

		if result.ExitCode == 0 {
			return events.write(loopResultEvent(loop, true, ""))
		}

		prompt = rocketcode.PromptInput{Role: rocketcode.PromptInputRoleDeveloper, Text: scriptFailureFeedback(result)}
	}
}

func runTurn(ctx context.Context, looper rocketcode.Looper, session *memorySession, prompt rocketcode.PromptInput, interrupts <-chan os.Signal, events *eventWriter, loop int, role string) error {
	input := make(chan rocketcode.PromptInput, 1)
	output := make(chan rocketcode.ChatResponse, 100)

	prompt.Responses = output
	input <- prompt

	close(input)

	var group errgroup.Group
	group.Go(func() error {
		return looper.Loop(ctx, input, session.in, func(entry rocketcode.SessionEntry) error { return session.out(&entry) }, interrupts)
	})
	group.Go(func() error {
		for item := range output {
			if err := events.write(chatResponseEvent(item, loop, role)); err != nil {
				return err
			}
		}

		return nil
	})

	if err := group.Wait(); err != nil {
		return fmt.Errorf("run %s turn: %w", role, err)
	}

	return nil
}

func runCritic(ctx context.Context, deps *runtimeDeps, goal string, claim goalClaim, events *eventWriter, loop int) (*criticVerdict, string, error) {
	criticSession := &memorySession{}
	criticSession.add(developerSessionEntry(criticInstructions()))

	var text strings.Builder

	collector := &eventWriter{w: writerFunc(func(data []byte) (int, error) {
		var event jsonlEvent
		if err := json.Unmarshal(bytes.TrimSpace(data), &event); err == nil && event.Text != "" {
			text.WriteString(event.Text)
			text.WriteString("\n")
		}

		return events.w.Write(data)
	})}

	prompt := rocketcode.PromptInput{Role: rocketcode.PromptInputRoleUser, Text: criticPrompt(goal, claim)}
	if err := runTurn(ctx, deps.criticLooper, criticSession, prompt, deps.interrupts, collector, loop, "critic"); err != nil {
		return nil, "", err
	}

	verdict := deps.verdicts.latest()
	if verdict != nil {
		if err := events.write(&jsonlEvent{Type: "critic_verdict", Loop: loop, Verdict: verdict, Succeeded: verdict.Approved}); err != nil {
			return nil, "", err
		}
	}

	return verdict, strings.TrimSpace(text.String()), nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(data []byte) (int, error) {
	return f(data)
}

func loopResultEvent(loop int, succeeded bool, errorText string) *jsonlEvent {
	return &jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: succeeded, Error: errorText}
}

func chatResponseEvent(item rocketcode.ChatResponse, loop int, role string) *jsonlEvent {
	return &jsonlEvent{Type: "chat_response", Loop: loop, Role: role, Kind: item.Kind, Text: item.Text, Tool: item.Tool, Subagent: item.Subagent, Provider: item.Provider}
}

func (w *eventWriter) write(event *jsonlEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if _, err := fmt.Fprintln(w.w, string(data)); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}

func newGoalTool(recorder *claimRecorder) rocketcode.Tool {
	return newInternalTool(
		"goal_achieved",
		"Call this only when the original looper goal is fully achieved. Provide a concise summary and concrete evidence.",
		map[string]any{"summary": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "string"}},
		[]string{"summary", "evidence"},
		func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var claim goalClaim
			if err := json.Unmarshal(raw, &claim); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("decode goal claim: %w", err)
			}

			recorder.set(&claim)

			return rocketcode.TextToolResult("goal completion claim recorded; external verification will run after this turn"), nil
		},
	)
}

func newCriticTool(recorder *verdictRecorder) rocketcode.Tool {
	return newInternalTool(
		"critic_verdict",
		"Call this with the final critic verdict for whether the original goal is fully achieved.",
		map[string]any{"approved": map[string]any{"type": "boolean"}, "feedback": map[string]any{"type": "string"}},
		[]string{"approved", "feedback"},
		func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var verdict criticVerdict
			if err := json.Unmarshal(raw, &verdict); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("decode critic verdict: %w", err)
			}

			recorder.set(&verdict)

			return rocketcode.TextToolResult("critic verdict recorded"), nil
		},
	)
}

func newInternalTool(name, description string, properties map[string]any, required []string, call func(context.Context, json.RawMessage, chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error)) rocketcode.Tool {
	return rocketcode.Tool{
		Name:        name,
		Description: description,
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		},
		Call: call,
	}
}

func (r *recorder[T]) set(value *T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.value = value
}

func (r *recorder[T]) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.value = nil
}

func (r *recorder[T]) latest() *T {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.value
}

func (s *memorySession) add(entry *rocketcode.SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, *entry)
}

func (s *memorySession) in(yield func(rocketcode.SessionEntry, error) bool) {
	s.mu.Lock()
	entries := append([]rocketcode.SessionEntry{}, s.entries...)
	s.mu.Unlock()

	for i := range entries {
		if !yield(entries[i], nil) {
			return
		}
	}
}

func (s *memorySession) out(entry *rocketcode.SessionEntry) error {
	s.add(entry)
	return nil
}

func developerSessionEntry(text string) *rocketcode.SessionEntry {
	input := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Role:    responses.EasyInputMessageRoleDeveloper,
		Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)},
		Type:    "message",
	}}

	replayInput, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{input})
	if err != nil {
		panic(err)
	}

	return &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInput}
}

func mainInstructions(goal string) string {
	return "You are running under cmd/looper. Work autonomously toward the original goal. Do not ask the user for interactive input. When and only when the original goal is fully achieved, call the goal_achieved tool with a concise summary and concrete evidence. The original goal is:\n\n" + goal
}

func criticInstructions() string {
	return "You are the cmd/looper critic. Verify whether the original goal is fully achieved. Use available tools when useful. You must call critic_verdict with approved=false and actionable feedback if anything is incomplete, unverified, or risky. Call critic_verdict with approved=true only when the goal is fully achieved."
}

func criticPrompt(goal string, claim goalClaim) string {
	return fmt.Sprintf("Original goal:\n%s\n\nMain agent completion summary:\n%s\n\nMain agent evidence:\n%s\n\nCheck the workspace and conversation evidence as needed, then call critic_verdict.", goal, claim.Summary, claim.Evidence)
}

func scriptFailureFeedback(result scriptResult) string {
	return fmt.Sprintf("The verification script failed. Continue working on the original goal.\n\nExit code: %d\n\nStdout:\n%s\n\nStderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
}

func runShellScript(ctx context.Context, cwd, script string, outputLimit int64) (scriptResult, error) {
	shell, args := shellCommand(script)
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = cwd

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := scriptResult{Command: script, ExitCode: 0, Stdout: limitOutput(stdout.String(), outputLimit), Stderr: limitOutput(stderr.String(), outputLimit)}
	if err == nil {
		return result, nil
	}

	if errExit, ok := errors.AsType[*exec.ExitError](err); ok {
		result.ExitCode = errExit.ExitCode()
		return result, nil
	}

	return result, fmt.Errorf("run script: %w", err)
}

func shellCommand(script string) (shell string, args []string) {
	shell = filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "bash":
		return "bash", []string{"-lc", script}
	case "zsh":
		return "zsh", []string{"-lc", script}
	default:
		return "sh", []string{"-c", script}
	}
}

func limitOutput(output string, limit int64) string {
	if limit <= 0 || int64(len(output)) <= limit {
		return output
	}

	return fmt.Sprintf("[truncated to last %d bytes]\n%s", limit, output[int64(len(output))-limit:])
}

func allowInternalTool(agents rocketcode.Agents, tool string) rocketcode.Agents {
	items := make(map[string]rocketcode.Agent, len(agents.Items))
	maps.Copy(items, agents.Items)

	agents.Items = items

	agent, ok := agents.Items[defaultAgent]
	if !ok {
		return agents
	}

	_ = agent.Permission.Allow("tools", tool)
	agents.Items[defaultAgent] = agent

	return agents
}

func promptInput(text string, root *os.Root, cwd string) (rocketcode.PromptInput, error) {
	text, files, err := rocketcode.SplitPromptAttachmentTokens(text)
	if err != nil {
		return rocketcode.PromptInput{}, rocketcode.OperationError{Operation: rocketcode.OperationParsePromptAttachments, Err: err}
	}

	attachments, err := rocketcode.PromptAttachments(root, cwd, files)
	if err != nil {
		return rocketcode.PromptInput{}, rocketcode.OperationError{Operation: rocketcode.OperationLoadPromptAttachments, Err: err}
	}

	if len(attachments) == 0 {
		attachments = nil
	}

	return rocketcode.PromptInput{Role: rocketcode.PromptInputRoleUser, Text: text, Attachments: attachments}, nil
}
