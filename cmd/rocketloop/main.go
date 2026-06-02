// Command looper runs RocketCode toward a non-interactive goal.
//
//nolint:exhaustruct,gocritic,wsl_v5,wrapcheck,perfsprint,modernize // Command wiring uses sparse SDK/runtime literals and favors local clarity.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

const defaultAgent = "main"
const maxAttachmentBytes = 5 * 1024 * 1024

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

type claimRecorder struct {
	mu    sync.Mutex
	claim *goalClaim
}

type verdictRecorder struct {
	mu      sync.Mutex
	verdict *criticVerdict
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

	return runAutonomousLoop(context.Background(), opt, deps, &eventWriter{w: stdout})
}

func parseOptions(args []string, stdin io.Reader) (options, error) {
	flags := flag.NewFlagSet("looper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var opt options
	flags.StringVar(&opt.script, "script", "", "shell command that returns 0 when the goal is achieved")
	flags.IntVar(&opt.maxLoops, "max-loops", 0, "maximum main-agent loops; 0 means unlimited")
	flags.Int64Var(&opt.scriptOutputLimit, "script-output-limit", 0, "maximum bytes kept from each script output stream; 0 means unlimited")

	if err := flags.Parse(args); err != nil {
		return options{}, err
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
	config, err := configFromEnv()
	if err != nil {
		return runtimeDeps{}, nil, err
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

	agentsRoot, agentsFS, err := fsFromRoot(root, "agents")
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, err
	}

	if agentsRoot != nil {
		oldCleanup := cleanup
		cleanup = func() { _ = agentsRoot.Close(); oldCleanup() }
	}

	skillsRoot, skillsFS, err := fsFromRoot(root, "skills")
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, err
	}

	if skillsRoot != nil {
		oldCleanup := cleanup
		cleanup = func() { _ = skillsRoot.Close(); oldCleanup() }
	}

	agents, skills := loadParsedAgentsAndSkills(agentsFS, skillsFS, skillsRootName(skillsRoot))
	client := openai.NewClient()

	claims := &claimRecorder{}
	verdicts := &verdictRecorder{}

	mainConfig := config
	mainConfig.CustomTools = append(mainConfig.CustomTools, newGoalTool(claims))
	mainAgents := allowInternalTool(agents, "goal_achieved")

	mainLooper, err := rocketcode.New(&client, mainConfig, root, mainAgents, skills, defaultAgent, diagnostics)
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, fmt.Errorf("initialize main rocketcode: %w", err)
	}

	criticConfig := config
	criticConfig.CustomTools = append(criticConfig.CustomTools, newCriticTool(verdicts))
	criticAgents := allowInternalTool(agents, "critic_verdict")

	criticLooper, err := rocketcode.New(&client, criticConfig, root, criticAgents, skills, defaultAgent, diagnostics)
	if err != nil {
		cleanup()
		return runtimeDeps{}, nil, fmt.Errorf("initialize critic rocketcode: %w", err)
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)

	oldCleanup := cleanup
	cleanup = func() {
		signal.Stop(interrupts)
		oldCleanup()
	}

	return runtimeDeps{mainLooper: mainLooper, criticLooper: criticLooper, root: root, cwd: cwd, interrupts: interrupts, runScript: runShellScript, claims: claims, verdicts: verdicts}, cleanup, nil
}

func runAutonomousLoop(ctx context.Context, opt options, deps runtimeDeps, events *eventWriter) error {
	mainSession := &memorySession{}
	mainSession.add(developerSessionEntry(mainInstructions(opt.goal)))

	prompt, err := promptInput(opt.goal, deps.root, deps.cwd)
	if err != nil {
		return err
	}

	for loop := 1; ; loop++ {
		if opt.maxLoops > 0 && loop > opt.maxLoops {
			_ = events.write(jsonlEvent{Type: "loop_result", Loop: loop - 1, Succeeded: false, Error: "max loops exhausted"})
			return errors.New("max loops exhausted")
		}

		deps.claims.clear()
		deps.verdicts.clear()

		if err := runTurn(ctx, deps.mainLooper, mainSession, prompt, deps.interrupts, events, loop, "main"); err != nil {
			_ = events.write(jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: false, Error: err.Error()})
			return err
		}

		claim := deps.claims.latest()
		if claim == nil {
			prompt = rocketcode.PromptInput{Role: rocketcode.PromptInputRoleDeveloper, Text: "You did not call goal_achieved. Continue working on the original goal, and call goal_achieved only when it is fully complete with concrete evidence."}
			continue
		}

		if err := events.write(jsonlEvent{Type: "goal_achieved", Loop: loop, Goal: claim}); err != nil {
			return err
		}

		verdict, criticText, err := runCritic(ctx, deps, opt.goal, *claim, events, loop)
		if err != nil {
			_ = events.write(jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: false, Error: err.Error()})
			return err
		}

		if verdict == nil || !verdict.Approved {
			feedback := criticText
			if verdict != nil {
				feedback = fmt.Sprintf("critic_verdict rejected the completion claim. approved=false\nfeedback:\n%s", verdict.Feedback)
			} else if feedback == "" {
				feedback = "The critic did not call critic_verdict. Continue working on the original goal and provide stronger evidence before calling goal_achieved again."
			}

			prompt = rocketcode.PromptInput{Role: rocketcode.PromptInputRoleDeveloper, Text: feedback}
			continue
		}

		if opt.script == "" {
			return events.write(jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: true})
		}

		result, err := deps.runScript(ctx, deps.cwd, opt.script, opt.scriptOutputLimit)
		if err != nil {
			_ = events.write(jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: false, Error: err.Error()})
			return err
		}

		if err := events.write(jsonlEvent{Type: "script_result", Loop: loop, Script: &result, Succeeded: result.ExitCode == 0}); err != nil {
			return err
		}

		if result.ExitCode == 0 {
			return events.write(jsonlEvent{Type: "loop_result", Loop: loop, Succeeded: true})
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
	group.Go(func() error { return looper.Loop(ctx, input, session.in, session.out, interrupts) })
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

func runCritic(ctx context.Context, deps runtimeDeps, goal string, claim goalClaim, events *eventWriter, loop int) (*criticVerdict, string, error) {
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
		if err := events.write(jsonlEvent{Type: "critic_verdict", Loop: loop, Verdict: verdict, Succeeded: verdict.Approved}); err != nil {
			return nil, "", err
		}
	}

	return verdict, strings.TrimSpace(text.String()), nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(data []byte) (int, error) { return f(data) }

func chatResponseEvent(item rocketcode.ChatResponse, loop int, role string) jsonlEvent {
	return jsonlEvent{Type: "chat_response", Loop: loop, Role: role, Kind: item.Kind, Text: item.Text, Tool: item.Tool, Subagent: item.Subagent, Provider: item.Provider}
}

func (w *eventWriter) write(event jsonlEvent) error {
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
	return rocketcode.Tool{
		Name:        "goal_achieved",
		Description: "Call this only when the original looper goal is fully achieved. Provide a concise summary and concrete evidence.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"evidence": map[string]any{"type": "string"},
			},
			"required":             []string{"summary", "evidence"},
			"additionalProperties": false,
		},
		Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var claim goalClaim
			if err := json.Unmarshal(raw, &claim); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("decode goal claim: %w", err)
			}

			recorder.set(&claim)
			return rocketcode.TextToolResult("goal completion claim recorded; external verification will run after this turn"), nil
		},
	}
}

func newCriticTool(recorder *verdictRecorder) rocketcode.Tool {
	return rocketcode.Tool{
		Name:        "critic_verdict",
		Description: "Call this with the final critic verdict for whether the original goal is fully achieved.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approved": map[string]any{"type": "boolean"},
				"feedback": map[string]any{"type": "string"},
			},
			"required":             []string{"approved", "feedback"},
			"additionalProperties": false,
		},
		Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var verdict criticVerdict
			if err := json.Unmarshal(raw, &verdict); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("decode critic verdict: %w", err)
			}

			recorder.set(&verdict)
			return rocketcode.TextToolResult("critic verdict recorded"), nil
		},
	}
}

func (r *claimRecorder) set(claim *goalClaim) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim = claim
}

func (r *claimRecorder) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim = nil
}

func (r *claimRecorder) latest() *goalClaim {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claim
}

func (r *verdictRecorder) set(verdict *criticVerdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verdict = verdict
}

func (r *verdictRecorder) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verdict = nil
}

func (r *verdictRecorder) latest() *criticVerdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verdict
}

func (s *memorySession) add(entry rocketcode.SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *memorySession) in(yield func(rocketcode.SessionEntry, error) bool) {
	s.mu.Lock()
	entries := append([]rocketcode.SessionEntry{}, s.entries...)
	s.mu.Unlock()

	for _, entry := range entries {
		if !yield(entry, nil) {
			return
		}
	}
}

func (s *memorySession) out(entry rocketcode.SessionEntry) error {
	s.add(entry)
	return nil
}

func developerSessionEntry(text string) rocketcode.SessionEntry {
	input := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Role:    responses.EasyInputMessageRoleDeveloper,
		Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)},
		Type:    "message",
	}}

	replayInput, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{input})
	if err != nil {
		panic(err)
	}

	return rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInput}
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

	var stdout bytes.Buffer
	var stderr bytes.Buffer
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

func shellCommand(script string) (string, []string) {
	shell := filepath.Base(os.Getenv("SHELL"))
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
	for name, agent := range agents.Items {
		items[name] = agent
	}

	agents.Items = items
	agent, ok := agents.Items[defaultAgent]
	if !ok {
		return agents
	}

	_ = agent.Permission.Allow("tools", tool)
	agents.Items[defaultAgent] = agent

	return agents
}

func loadParsedAgentsAndSkills(agentsFS, skillsFS fs.FS, skillsRoot string) (rocketcode.Agents, rocketcode.Skills) {
	agentResult := rocketcode.LoadAgents(agentsFS)
	skillResult := rocketcode.LoadSkills(skillsFS, skillsRoot)

	return agentResult.Agents, skillResult.Skills
}

func configFromEnv() (rocketcode.Config, error) {
	config := defaultConfig()

	if value := os.Getenv("ROCKETCODE_MODEL"); value != "" {
		config.Model = value
	}

	if value := os.Getenv("ROCKETCODE_REASONING_EFFORT"); value != "" {
		config.ReasoningEffort = shared.ReasoningEffort(value)
	}

	config.Diagnostics = os.Getenv("ROCKETCODE_DIAG") != ""
	config.ExperimentalStrongerSkills = os.Getenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS") != ""

	expansion := strings.TrimSpace(os.Getenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS"))
	switch {
	case expansion == "" || expansion == "0" || strings.EqualFold(expansion, "false"):
		config.ExpandPromptShellCommands = rocketcode.PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}
	case expansion == "1" || strings.EqualFold(expansion, "true"):
		config.ExpandPromptShellCommands = rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}
	default:
		config.ExpandPromptShellCommands = rocketcode.PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}

		for part := range strings.SplitSeq(expansion, ",") {
			token := strings.ToLower(strings.TrimSpace(part))
			switch token {
			case "":
				continue
			case "all":
				config.ExpandPromptShellCommands.PrimaryPrompts = true
				config.ExpandPromptShellCommands.SubagentPrompts = true
				config.ExpandPromptShellCommands.SkillPrompts = true
			case "primary":
				config.ExpandPromptShellCommands.PrimaryPrompts = true
			case "subagent":
				config.ExpandPromptShellCommands.SubagentPrompts = true
			case "skill":
				config.ExpandPromptShellCommands.SkillPrompts = true
			case "input":
				config.ExpandPromptShellCommands.InputPrompts = true
			default:
				return rocketcode.Config{}, fmt.Errorf("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS contains unknown value %q: expected primary, subagent, skill, input, or all", token)
			}
		}
	}

	if value := os.Getenv("ROCKETCODE_COMPACT_THRESHOLD"); value != "" {
		threshold, err := strconv.ParseInt(value, 10, 64)
		if err != nil || threshold <= 0 {
			return rocketcode.Config{}, errors.New("ROCKETCODE_COMPACT_THRESHOLD must be a positive integer")
		}

		config.CompactThreshold = threshold
	}

	config.CompactionSteering = os.Getenv("ROCKETCODE_COMPACTION_STEERING")

	return config, nil
}

func defaultConfig() rocketcode.Config {
	return rocketcode.Config{
		Model:                      openai.ChatModelGPT5_4,
		ReasoningEffort:            shared.ReasoningEffort("high"),
		Diagnostics:                false,
		ExperimentalStrongerSkills: false,
		ExpandPromptShellCommands:  rocketcode.PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false},
		CompactThreshold:           200000,
		CompactionSteering:         "",
		ShellOutputDir:             filepath.Join(".tmp", "shell-outputs"),
		SandboxedBash:              false,
		ShellEnv:                   nil,
		CustomTools: []rocketcode.Tool{{
			Name:               "current_time",
			Permission:         "",
			VisibilitySubjects: nil,
			Subjects:           nil,
			Description:        "Tell the current time anywhere in the world.",
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"required":             []string{},
				"additionalProperties": false,
			},
			Call: func(context.Context, json.RawMessage, chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
				return rocketcode.TextToolResult(time.Now().String()), nil
			},
		}},
	}
}

func fsFromRoot(root *os.Root, name string) (*os.Root, fs.FS, error) {
	child, err := root.OpenRoot(name)
	if err == nil {
		return child, child.FS(), nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return nil, emptyFS{}, nil
	}

	return nil, nil, fmt.Errorf("open %s root: %w", name, err)
}

type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if name == "." {
		return emptyDir{}, nil
	}

	return nil, fs.ErrNotExist
}

type emptyDir struct{}

func (emptyDir) Close() error { return nil }

func (emptyDir) Read([]byte) (int, error) { return 0, io.EOF }

func (emptyDir) Stat() (fs.FileInfo, error) { return emptyDirInfo{}, nil }

func (emptyDir) ReadDir(int) ([]fs.DirEntry, error) { return nil, nil }

type emptyDirInfo struct{}

func (emptyDirInfo) Name() string { return "." }

func (emptyDirInfo) Size() int64 { return 0 }

func (emptyDirInfo) Mode() fs.FileMode { return fs.ModeDir | 0o755 }

func (emptyDirInfo) ModTime() time.Time { return time.Time{} }

func (emptyDirInfo) IsDir() bool { return true }

func (emptyDirInfo) Sys() any { return nil }

func skillsRootName(root *os.Root) string {
	if root == nil {
		return ""
	}

	return root.Name()
}

func promptInput(text string, root *os.Root, cwd string) (rocketcode.PromptInput, error) {
	parts := strings.Fields(text)
	files := []string{}
	kept := make([]string, 0, len(parts))

	for _, part := range parts {
		path, ok := strings.CutPrefix(part, "@attach:")
		if ok {
			if path == "" {
				return rocketcode.PromptInput{}, errors.New("@attach requires a file path")
			}

			files = append(files, path)
			continue
		}

		kept = append(kept, part)
	}

	attachments, err := promptAttachments(root, cwd, files)
	if err != nil {
		return rocketcode.PromptInput{}, err
	}

	if len(attachments) == 0 {
		attachments = nil
	}

	return rocketcode.PromptInput{Role: rocketcode.PromptInputRoleUser, Text: strings.Join(kept, " "), Attachments: attachments}, nil
}

func promptAttachments(root *os.Root, cwd string, files []string) ([]rocketcode.Attachment, error) {
	attachments := make([]rocketcode.Attachment, 0, len(files))
	for _, file := range files {
		name := file
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}

		abs, err := filepath.Abs(name)
		if err != nil {
			return nil, fmt.Errorf("resolve attachment %q: %w", file, err)
		}

		rel, err := filepath.Rel(cwd, abs)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("attachment %q is outside the workspace", file)
		}

		data, err := root.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", file, err)
		}

		mimeType := sniffAttachmentMIME(data, rel)
		attachment, err := attachmentFromBytes(filepath.Base(rel), mimeType, data)
		if err != nil {
			return nil, fmt.Errorf("attach %q: %w", file, err)
		}

		attachments = append(attachments, attachment)
	}

	return attachments, nil
}

func attachmentFromBytes(filename, mimeType string, data []byte) (rocketcode.Attachment, error) {
	if len(data) > maxAttachmentBytes {
		return rocketcode.Attachment{}, errors.New("attachment too large (exceeds 5MB limit)")
	}

	mimeType = normalizeMIME(mimeType)
	if !isSupportedAttachmentMIME(mimeType) {
		return rocketcode.Attachment{}, fmt.Errorf("unsupported attachment MIME type: %s", mimeType)
	}

	return rocketcode.Attachment{MIME: mimeType, Filename: filename, URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)}, nil
}

func sniffAttachmentMIME(data []byte, filename string) string {
	mimeType := normalizeMIME(http.DetectContentType(data))
	if isSupportedAttachmentMIME(mimeType) {
		return mimeType
	}

	return mimeFromFilename(filename)
}

func normalizeMIME(mimeType string) string {
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

func isSupportedAttachmentMIME(mimeType string) bool {
	mimeType = normalizeMIME(mimeType)
	return mimeType == "application/pdf" || strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml" && mimeType != "image/vnd.fastbidsheet"
}

func mimeFromFilename(filename string) string {
	if ext := filepath.Ext(filename); ext != "" {
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return normalizeMIME(mimeType)
		}
	}

	return "application/octet-stream"
}
