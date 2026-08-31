package quickbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
)

// CaptureOptions configures Capture.
type CaptureOptions struct {
	DatabaseURL    string
	ConversationID string
	AgentsDir      string // workspace agents/ directory (required for full tree)
	Root           string // root agent name; default main
	Out            string
	Variation      string
	Pack           bool
}

const (
	stubCriteria = "TODO: replace with ranking criteria before meaningful ELO.\n"
	stubJudge    = "gpt-5.6-luna?reasoningEffort=max"
)

// Capture builds a BAR from RocketClaw session entries.
func Capture(ctx context.Context, opt CaptureOptions) error {
	conversationID := strings.TrimSpace(opt.ConversationID)
	if conversationID == "" {
		return errors.New("conversation ID is required")
	}

	databaseURL := strings.TrimSpace(opt.DatabaseURL)
	if databaseURL == "" {
		return errors.New("database_url is required")
	}

	variation := strings.TrimSpace(opt.Variation)
	if variation == "" {
		variation = "captured"
	}

	root := strings.TrimSpace(opt.Root)
	if root == "" {
		root = "main"
	}

	agentsDir := strings.TrimSpace(opt.AgentsDir)
	if agentsDir == "" {
		return errors.New("--agents directory is required (copy workspace agents/*.md verbatim)")
	}

	agents, err := copyWorkspaceAgents(agentsDir)
	if err != nil {
		return err
	}

	if _, ok := agents[root]; !ok {
		return fmt.Errorf("root agent %q not found in %s", root, agentsDir)
	}

	entries, err := backend.ObserveSessionEntries(ctx, databaseURL, conversationID, 0)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return fmt.Errorf("no session entries for conversation %q", conversationID)
	}

	transcript, tools, bashDoubles, err := extractFromEntries(entries)
	if err != nil {
		return err
	}

	if err := validateTranscript(transcript); err != nil {
		return fmt.Errorf("captured transcript: %w", err)
	}
	// Validate task targets against copied agents before write.
	if _, _, err := buildAgents(&BAR{Meta: Meta{Root: root}, Agents: agents}, Variation{}, nil); err != nil {
		return err
	}

	bar := &BAR{
		Meta: Meta{
			Name:        variation,
			Description: "captured from " + conversationID,
			Root:        root,
		},
		Agents: agents,
		Variations: []Variation{{
			ID:          variation,
			Transcript:  transcript,
			Tools:       tools,
			BashDoubles: bashDoubles,
		}},
		Criteria: stubCriteria,
		Judge:    strings.TrimSpace(stubJudge),
	}

	out := opt.Out
	if out == "" {
		if opt.Pack {
			out = variation + ".bar"
		} else {
			out = variation
		}
	}

	if opt.Pack || strings.HasSuffix(strings.ToLower(out), ".bar") {
		dir, err := scratchDir("capture-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()

		if err := WriteDir(dir, bar); err != nil {
			return err
		}

		return Pack(dir, out)
	}

	return WriteDir(out, bar)
}

func extractFromEntries(entries []backend.ObservedSessionEntry) ([]Message, []ToolMock, []BashDouble, error) {
	var messages []Message

	toolOut := map[string]string{}
	toolArgs := map[string]string{} // call_id -> name
	toolArgRaw := map[string]string{}

	var bashObs []observedBash

	pendingBash := map[string][]string{} // call_id -> commands

	for _, observed := range entries {
		entry := observed.Entry
		for _, raw := range entry.ReplayInput {
			kind, err := replayKind(raw)
			if err != nil {
				continue
			}

			switch kind {
			case "message":
				role, text, err := replayMessage(raw)
				if err != nil || text == "" {
					continue
				}

				if role != "user" && role != "assistant" {
					continue
				}

				messages = append(messages, Message{Role: role, Text: text})
			case "function_call":
				name, callID, args, err := replayFunctionCall(raw)
				if err != nil {
					continue
				}

				if callID != "" {
					toolArgs[callID] = name
					toolArgRaw[callID] = args
				}

				switch name {
				case "shell":
					if cmd := parseShellCommandFromToolArgs(args); cmd != "" {
						pendingBash[callID] = []string{cmd}
					}
				case "execute":
					if cmds := parseShellCommandsFromExecuteArgs(args); len(cmds) > 0 {
						pendingBash[callID] = cmds
					}
				}
			case "function_call_output":
				callID, output, err := replayFunctionOutput(raw)
				if err != nil {
					continue
				}

				name := toolArgs[callID]
				if name == "" {
					name = "tool"
				}

				if cmds := pendingBash[callID]; len(cmds) > 0 {
					for _, cmd := range cmds {
						bashObs = append(bashObs, observedBash{command: cmd, output: output})
						messages = append(messages, Message{Role: "assistant", Text: "[" + name + "] " + cmd + "\n" + output})
					}

					delete(pendingBash, callID)
				} else {
					messages = append(messages, Message{Role: "assistant", Text: "[" + name + "]\n" + output})
				}

				if name == "task" || name == "shell" || name == "execute" {
					continue
				}

				toolOut[name] = output
			}
		}
	}

	if len(messages) == 0 {
		return nil, nil, nil, errors.New("no user/assistant messages in session")
	}

	var tools []ToolMock
	for name, response := range toolOut {
		tools = append(tools, ToolMock{
			Name:        name,
			Description: "captured static mock for " + name,
			Parameters:  map[string]any{"type": "object"},
			Response:    response,
		})
	}

	slices.SortFunc(tools, func(a, b ToolMock) int {
		return strings.Compare(a.Name, b.Name)
	})

	return messages, tools, extractBashDoubles(bashObs), nil
}

func replayKind(raw json.RawMessage) (string, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", err
	}

	if head.Type == "" {
		return "message", nil
	}

	return head.Type, nil
}

func replayMessage(raw json.RawMessage) (role, text string, err error) {
	var msg struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", "", err
	}

	role = msg.Role
	if len(msg.Content) == 0 {
		return role, "", nil
	}
	// content may be string or array
	var asString string
	if err := json.Unmarshal(msg.Content, &asString); err == nil {
		return role, asString, nil
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Content, &parts); err != nil {
		return role, string(msg.Content), nil
	}

	var b strings.Builder

	for _, p := range parts {
		if p.Text != "" {
			b.WriteString(p.Text)
		}
	}

	return role, b.String(), nil
}

func replayFunctionCall(raw json.RawMessage) (name, callID, args string, err error) {
	var call struct {
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return "", "", "", err
	}

	return call.Name, call.CallID, call.Arguments, nil
}

func replayFunctionOutput(raw json.RawMessage) (callID, output string, err error) {
	var out struct {
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}

	if len(out.Output) == 0 {
		return out.CallID, "", nil
	}

	var asString string
	if err := json.Unmarshal(out.Output, &asString); err == nil {
		return out.CallID, asString, nil
	}
	// array of content parts
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out.Output, &parts); err == nil {
		var b strings.Builder

		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}

		return out.CallID, b.String(), nil
	}

	return out.CallID, string(out.Output), nil
}
