package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

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
	bus             *events.Bus
	renderer        *terminalRenderer
	in              io.Reader
	reader          *bufio.Reader
	agent           string
	conversationID  string
	newConversation bool
	onExit          func()
	submit          func(context.Context, *events.InboundMessage) error
	summarize       func(context.Context, string) (string, error)
	publishMain     func(context.Context, *events.InboundMessage) error
}

type terminalRenderer struct {
	out io.Writer
}

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
		conversationID = "cli:" + rand.Text()
	}

	return &terminalCLI{
		bus: bus, renderer: &terminalRenderer{out: options.Out}, in: options.In, reader: bufio.NewReader(options.In),
		agent: agent, conversationID: conversationID, newConversation: options.NewConversation, onExit: onExit,
		summarize: func(context.Context, string) (string, error) { return "", nil }, publishMain: func(context.Context, *events.InboundMessage) error { return nil },
	}
}

func (c *terminalCLI) Start(ctx context.Context) {
	c.renderer.printLine("terminal CLI attached to " + c.conversationID)

	go c.observe(ctx)
	go c.readInput(ctx)
}
func (c *terminalCLI) observe(ctx context.Context) {
	for observed := range c.bus.Observe(ctx) {
		if observed.Inbound != nil && observed.Inbound.ConversationID == c.conversationID {
			c.renderer.printEvent(formatInbound(observed.Inbound))
		}

		if observed.Outbound != nil && observed.Outbound.ConversationID == c.conversationID {
			c.renderer.printEvent(formatOutbound(observed.Outbound))
		}
	}
}

func (c *terminalCLI) readInput(ctx context.Context) {
	errRead := c.readLines(ctx, func(line string) error {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}

		if line == "/exit" {
			return io.EOF
		}

		inbound := events.NewMainInboundMessage(events.SourceTerminalCLI, events.InboundKindPrompt, "terminal", line, true)

		inbound.ConversationID = c.conversationID
		inbound.Metadata = map[string]string{events.TerminalCLIClientIDMetadataKey: "embedded"}
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

func (c *terminalCLI) readLines(_ context.Context, submit func(string) error) error {
	for {
		line, err := c.reader.ReadString('\n')
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

	summary, _ := c.summarize(context.Background(), "Summarize this terminal CLI session for the main RocketClaw conversation. Include decisions, useful context, and unresolved follow-ups.")

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

func replayMessagePreview(raw json.RawMessage) (role, text string) {
	var object struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", ""
	}

	if content, ok := object.Content.(string); ok {
		return object.Role, strings.Join(strings.Fields(content), " ")
	}

	return "", ""
}

func formatInbound(msg *events.InboundMessage) string {
	return fmt.Sprintf("[%s inbound] %s", msg.Source, strings.TrimSpace(msg.Text))
}

func formatOutbound(msg *events.OutboundMessage) string {
	return "[assistant] " + strings.TrimSpace(msg.Text)
}

func (r *terminalRenderer) printLine(text string) {
	_, _ = fmt.Fprintln(r.out, text)
}

func (r *terminalRenderer) printEvent(text string) {
	_, _ = fmt.Fprintln(r.out, text)
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

	if req.Multiple {
		r.printLine("Enter one or more option numbers separated by comma or space, or type a custom answer.")
	} else if len(req.Options) > 0 {
		r.printLine("Enter an option number, or type a custom answer.")
	} else {
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

	separators := func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }
	fields := strings.FieldsFunc(line, separators)
	if len(fields) == 0 {
		answer.Custom = line
		return answer, nil
	}

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
