package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

// CLIOptions configures the terminal connector for RunCLI.
type CLIOptions struct {
	In              io.Reader
	Out             io.Writer
	Agent           string
	ConversationID  string
	NewConversation bool
}

type terminalCLI struct {
	bus               *events.Bus
	renderer          *terminalRenderer
	reader            *bufio.Reader
	agent             string
	conversationID    string
	newConversation   bool
	onExit            func()
	submit            func(context.Context, *events.InboundMessage) error
	cmux              func(context.Context, ...string) (string, error)
	newConversationID func(context.Context, string) (string, error)
	summarize         func(context.Context, string) (string, error)
	publishMain       func(context.Context, *events.InboundMessage) error
}

type terminalRenderer struct {
	out           io.Writer
	lastAssistant string
	interactive   bool
	promptActive  bool
	mu            sync.Mutex
}

const terminalCLIHelpText = `available commands:
  /help         Show terminal CLI commands.
  /exit         Close this terminal CLI session.
  /new [agent]  In cmux, open a new terminal surface attached to a fresh private CLI conversation.`

func newTerminalCLI(options CLIOptions, bus *events.Bus, onExit func()) *terminalCLI {
	agent := strings.TrimSpace(options.Agent)
	if agent == "" {
		agent = "main"
	}

	conversationID := strings.TrimSpace(options.ConversationID)
	if conversationID == "" {
		conversationID = events.MainConversationID()
	}

	if options.NewConversation {
		conversationID = newTerminalConversationID()
	}

	return &terminalCLI{bus: bus, renderer: newTerminalRenderer(options.Out), reader: bufio.NewReader(options.In), agent: agent, conversationID: conversationID, newConversation: options.NewConversation, onExit: onExit, cmux: runCMUX, newConversationID: func(context.Context, string) (string, error) {
		return "", errors.New("/new requires a running RocketClaw server control socket")
	}, summarize: func(context.Context, string) (string, error) { return "", nil }, publishMain: func(context.Context, *events.InboundMessage) error { return nil }}
}

func newTerminalRenderer(out io.Writer) *terminalRenderer {
	r := &terminalRenderer{out: out}
	if file, ok := out.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			r.interactive = true
		}
	}

	return r
}

func runCMUX(ctx context.Context, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, "cmux", args...).CombinedOutput()
	return string(output), err
}

func (c *terminalCLI) Start(ctx context.Context) {
	c.renderer.printLine("terminal CLI attached to " + c.conversationID)

	go c.observe(ctx)
	go c.readInput(ctx)
}

func (c *terminalCLI) observe(ctx context.Context) {
	for observed := range c.bus.Observe(ctx) {
		if observed.Inbound != nil && observed.Inbound.ConversationID == c.conversationID {
			c.renderer.printLine(formatInbound(observed.Inbound))
		}

		if observed.Outbound != nil && observed.Outbound.ConversationID == c.conversationID {
			c.renderer.printLine(formatOutbound(observed.Outbound))
		}
	}
}

func (c *terminalCLI) readInput(ctx context.Context) {
	errRead := c.readLines(func(line string) error {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}

		if handled, err := c.handleSlashCommand(ctx, line); handled || err != nil {
			return err
		}

		inbound := events.NewMainInboundMessage(events.SourceTerminalCLI, events.InboundKindPrompt, "terminal", line, true)
		inbound.ConversationID = c.conversationID

		inbound.Metadata = map[string]string{events.TerminalCLIClientIDMetadataKey: events.TerminalCLIEmbeddedClientID}
		disableStartNewThread(inbound)

		if principal := terminalPrincipal(events.TerminalCLIEmbeddedClientID); principal != "" {
			inbound.Metadata[events.InboundPrincipalMetadataKey] = principal
		}

		if err := c.submit(ctx, inbound); err != nil {
			return fmt.Errorf("submit terminal prompt: %w", err)
		}

		return nil
	})
	if errRead != nil && !errors.Is(errRead, io.EOF) && ctx.Err() == nil {
		c.renderer.printLine("input stopped: " + errRead.Error())
	}

	if c.newConversation && ctx.Err() == nil {
		c.offerSummary()
	}

	c.onExit()
}

func terminalPrincipal(clientID string) string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}

	return strings.TrimSpace(clientID)
}

func (c *terminalCLI) handleSlashCommand(ctx context.Context, line string) (bool, error) {
	if !strings.HasPrefix(line, "/") {
		return false, nil
	}

	command, rest, _ := strings.Cut(line, " ")
	switch command {
	case "/exit":
		return true, io.EOF
	case "/help":
		c.renderer.printLine(terminalCLIHelpText)
		return true, nil
	case "/new":
		if err := c.openCMUXConversation(ctx, strings.TrimSpace(rest)); err != nil {
			c.renderer.printLine(err.Error())
		}

		return true, nil
	default:
		c.renderer.printLine("unknown command " + command + "; available commands: /exit, /new [agent]")
		return true, nil
	}
}

func (c *terminalCLI) openCMUXConversation(ctx context.Context, agent string) error {
	workspaceID, workingDirectory, err := c.cmuxContext(ctx)
	if err != nil {
		return err
	}

	conversationID, err := c.newConversationID(ctx, agent)
	if err != nil {
		return fmt.Errorf("create CLI conversation: %w", err)
	}

	return c.openCMUXAttachedInContext(ctx, conversationID, workspaceID, workingDirectory)
}

func (c *terminalCLI) openCMUXAttached(ctx context.Context, conversationID string) error {
	workspaceID, workingDirectory, err := c.cmuxContext(ctx)
	if err != nil {
		return err
	}

	return c.openCMUXAttachedInContext(ctx, conversationID, workspaceID, workingDirectory)
}

func (c *terminalCLI) cmuxContext(ctx context.Context) (workspaceID, workingDirectory string, err error) {
	if _, err := c.cmux(ctx, "identify"); err != nil {
		return "", "", fmt.Errorf("/new only opens a new cmux terminal. Outside cmux, run `rocketclaw cli --new [agent]` in another terminal. cmux identify failed: %w", err)
	}

	workspaceID, workingDirectory = os.Getenv("CMUX_WORKSPACE_ID"), os.Getenv("CMUX_WORKING_DIRECTORY")
	if os.Getenv("CMUX_SURFACE_ID") == "" || workingDirectory == "" {
		return "", "", errors.New("/new only opens a new cmux terminal. Outside cmux, run `rocketclaw cli --new [agent]` in another terminal")
	}

	return workspaceID, workingDirectory, nil
}

func (c *terminalCLI) openCMUXAttachedInContext(ctx context.Context, conversationID, workspaceID, workingDirectory string) error {
	newSurfaceArgs := []string{"new-surface", "--type", "terminal", "--working-directory", workingDirectory, "--focus", "true"}
	if workspaceID != "" {
		newSurfaceArgs = append(newSurfaceArgs, "--workspace", workspaceID)
	}

	surface, err := c.cmux(ctx, newSurfaceArgs...)
	if err != nil {
		return fmt.Errorf("create cmux terminal surface: %w", err)
	}

	surfaceID := strings.TrimSpace(surface)
	if _, err := c.cmux(ctx, "send", "--surface", surfaceID, "rocketclaw cli --attach "+conversationID+"\n"); err != nil {
		return fmt.Errorf("send attach command to cmux surface: %w", err)
	}

	return nil
}

func (c *terminalCLI) readLines(submit func(string) error) error {
	for {
		c.renderer.printPrompt()
		line, err := c.reader.ReadString('\n')
		c.renderer.finishPrompt()

		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read terminal input: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			if err := submit(line); err != nil {
				return err
			}
		}

		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (c *terminalCLI) offerSummary() {
	if !c.askYesNo("Append a summary of this CLI session to main? [y/N] ") {
		return
	}

	summary, err := c.summarize(context.Background(), "Summarize this terminal CLI session for the main RocketClaw conversation. Include decisions, useful context, and unresolved follow-ups.")
	if err != nil {
		c.renderer.printLine("summary failed: " + err.Error())
		return
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		c.renderer.printLine("summary was empty; main was not updated")
		return
	}

	inbound := events.NewMainInboundMessage(events.SourceTerminalCLI, events.InboundKindInternalize, "terminal_cli_summary", summary, false)
	if err := c.publishMain(context.Background(), inbound); err != nil {
		c.renderer.printLine("append summary failed: " + err.Error())
	}
}

func (c *terminalCLI) askYesNo(prompt string) bool {
	if _, err := fmt.Fprint(c.renderer.out, prompt); err != nil {
		return false
	}

	line, err := c.reader.ReadString('\n')

	return err == nil && strings.EqualFold(strings.TrimSpace(line), "y")
}

func (c *terminalCLI) askUserQuestion(_ context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
	c.renderer.printQuestion(req)

	line, err := c.reader.ReadString('\n')
	if err != nil {
		return events.AskUserQuestionAnswer{}, fmt.Errorf("read terminal question answer: %w", err)
	}

	return terminalQuestionAnswer(req, line)
}

func formatInbound(msg *events.InboundMessage) string {
	if msg.Source == events.SourceTerminalCLI {
		return "[you] " + strings.TrimSpace(msg.Text)
	}

	return fmt.Sprintf("[%s inbound] %s", msg.Source, strings.TrimSpace(msg.Text))
}

func formatOutbound(msg *events.OutboundMessage) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return "[assistant] (empty response)"
	}

	return "[assistant] " + text
}

func (r *terminalRenderer) printLine(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if strings.HasPrefix(text, "[assistant] ") {
		if text == r.lastAssistant {
			return
		}

		r.lastAssistant = text
	} else {
		r.lastAssistant = ""
	}

	if r.promptActive {
		_, _ = fmt.Fprint(r.out, "\r\033[2K")
	}

	_, _ = fmt.Fprintln(r.out, text)
	if r.promptActive {
		_, _ = fmt.Fprint(r.out, "rocketclaw> ")
	}
}

func (r *terminalRenderer) printPrompt() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.interactive {
		return
	}

	r.promptActive = true
	_, _ = fmt.Fprint(r.out, "rocketclaw> ")
}

func (r *terminalRenderer) finishPrompt() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.promptActive = false
}

func (r *terminalRenderer) printQuestion(req *events.AskUserQuestionRequest) {
	if req == nil {
		return
	}

	r.printLine("[question] " + strings.TrimSpace(req.Question))

	if details := strings.TrimSpace(req.Details); details != "" {
		r.printLine(details)
	}

	for i, option := range req.Options {
		line := fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(option.Label))
		if description := strings.TrimSpace(option.Description); description != "" {
			line += " - " + description
		}

		r.printLine(line)
	}

	switch {
	case req.Multiple:
		r.printLine("Enter one or more option numbers separated by comma or space, or type a custom answer.")
	case len(req.Options) > 0:
		r.printLine("Enter an option number, or type a custom answer.")
	default:
		r.printLine("Type your answer.")
	}
}

func terminalQuestionAnswer(req *events.AskUserQuestionRequest, line string) (events.AskUserQuestionAnswer, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return events.AskUserQuestionAnswer{}, errors.New("answer cannot be empty")
	}

	answer := events.AskUserQuestionAnswer{Source: events.SourceTerminalCLI}
	if len(req.Options) == 0 {
		answer.Custom = line
		return answer, nil
	}

	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })

	selected := make([]string, 0, len(fields))
	for _, field := range fields {
		index, err := strconv.Atoi(field)
		if err != nil {
			answer.Custom = line
			return answer, nil
		}

		if index < 1 || index > len(req.Options) {
			return events.AskUserQuestionAnswer{}, fmt.Errorf("option %d is out of range", index)
		}

		selected = append(selected, req.Options[index-1].Value)
	}

	if !req.Multiple && len(selected) > 1 {
		return events.AskUserQuestionAnswer{}, errors.New("select only one option")
	}

	answer.Selected = selected

	return answer, nil
}
