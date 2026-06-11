// Command rocketcode starts RocketCode in CLI mode.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"golang.org/x/sync/errgroup"
	_ "modernc.org/sqlite"
)

const defaultAgent = "main"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	config, err := rocketcode.StandaloneConfigFromEnv()
	if err != nil {
		return rocketcode.OperationError{Operation: rocketcode.OperationLoadConfig, Err: err}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	root, err := os.OpenRoot(cwd)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	if err := root.MkdirAll(config.ShellOutputDir, 0o755); err != nil {
		return fmt.Errorf("create shell output dir: %w", err)
	}

	config.ShellOutputDir = filepath.Join(cwd, config.ShellOutputDir)

	agents, skills, cleanupDefinitions, err := rocketcode.LoadWorkspaceDefinitions(root)
	if err != nil {
		return rocketcode.OperationError{Operation: rocketcode.OperationLoadWorkspaceDefinitions, Err: err}
	}
	defer cleanupDefinitions()

	session, err := openSession(root, cwd)
	if err != nil {
		return err
	}

	defer func() { _ = session.close() }()

	interrupts := make(chan os.Signal, 1)

	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	input := make(chan rocketcode.PromptInput)

	var group errgroup.Group
	group.Go(func() error { return scanInput(os.Stdin, os.Stdout, input, root, cwd) })

	client := openai.NewClient()

	looper, err := rocketcode.New(&client, &config, root, agents, skills, defaultAgent, os.Stdout)
	if err != nil {
		return fmt.Errorf("initialize rocketcode: %w", err)
	}

	if err := looper.Loop(context.Background(), input, session.in, session.out, interrupts); err != nil {
		return fmt.Errorf("run rocketcode: %w", err)
	}

	if err := group.Wait(); err != nil {
		return fmt.Errorf("wait for terminal io: %w", err)
	}

	return nil
}

type sessionStore struct {
	in    iter.Seq2[rocketcode.SessionEntry, error]
	out   func(rocketcode.SessionEntry) error
	close func() error
}

func openSession(root *os.Root, cwd string) (sessionStore, error) {
	if err := root.MkdirAll(".tmp", 0o755); err != nil {
		return sessionStore{}, fmt.Errorf("create temp dir: %w", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(cwd, ".tmp", "session.sqlite"))
	if err != nil {
		return sessionStore{}, fmt.Errorf("open session database: %w", err)
	}

	if err := initializeSessionDB(db); err != nil {
		_ = db.Close()

		return sessionStore{}, err
	}

	return sessionStore{
		in:    sqliteSessionIn(db),
		out:   sqliteSessionOut(db),
		close: db.Close,
	}, nil
}

func initializeSessionDB(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS session_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	version INTEGER NOT NULL,
	type TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	response_id TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	replay_input_json TEXT NOT NULL DEFAULT '[]',
	output_trace_json TEXT NOT NULL DEFAULT '[]'
)`)
	if err != nil {
		return fmt.Errorf("initialize session database: %w", err)
	}

	return nil
}

func sqliteSessionIn(db *sql.DB) iter.Seq2[rocketcode.SessionEntry, error] {
	return func(yield func(rocketcode.SessionEntry, error) bool) {
		rows, err := db.Query("SELECT version, type, timestamp, response_id, model, replay_input_json, output_trace_json FROM session_entries ORDER BY id")
		if err != nil {
			var zero rocketcode.SessionEntry
			yield(zero, fmt.Errorf("query session entries: %w", err))

			return
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var (
				entry       rocketcode.SessionEntry
				timestamp   string
				replayInput []byte
				outputTrace []byte
			)

			if err := rows.Scan(&entry.Version, &entry.Type, &timestamp, &entry.ResponseID, &entry.Model, &replayInput, &outputTrace); err != nil {
				var zero rocketcode.SessionEntry
				yield(zero, fmt.Errorf("scan session entry: %w", err))

				return
			}

			parsed, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				var zero rocketcode.SessionEntry
				yield(zero, fmt.Errorf("parse session timestamp %q: %w", timestamp, err))

				return
			}

			entry.Timestamp = parsed

			if err := json.Unmarshal(replayInput, &entry.ReplayInput); err != nil {
				var zero rocketcode.SessionEntry
				yield(zero, fmt.Errorf("decode session replay input: %w", err))

				return
			}

			if err := json.Unmarshal(outputTrace, &entry.OutputTrace); err != nil {
				var zero rocketcode.SessionEntry
				yield(zero, fmt.Errorf("decode session output trace: %w", err))

				return
			}

			if !yield(entry, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			var zero rocketcode.SessionEntry
			yield(zero, fmt.Errorf("iterate session entries: %w", err))
		}
	}
}

func sqliteSessionOut(db *sql.DB) func(rocketcode.SessionEntry) error {
	return func(entry rocketcode.SessionEntry) error {
		if _, err := rocketcode.ReplayInputToParams(entry.ReplayInput); err != nil {
			return fmt.Errorf("validate session replay input: %w", err)
		}

		replayInput, err := json.Marshal(entry.ReplayInput)
		if err != nil {
			return fmt.Errorf("encode session replay input: %w", err)
		}

		outputTrace, err := json.Marshal(entry.OutputTrace)
		if err != nil {
			return fmt.Errorf("encode session output trace: %w", err)
		}

		_, err = db.Exec(
			"INSERT INTO session_entries (version, type, timestamp, response_id, model, replay_input_json, output_trace_json) VALUES (?, ?, ?, ?, ?, ?, ?)",
			entry.Version,
			entry.Type,
			entry.Timestamp.UTC().Format(time.RFC3339Nano),
			entry.ResponseID,
			entry.Model,
			string(replayInput),
			string(outputTrace),
		)
		if err != nil {
			return fmt.Errorf("insert session entry: %w", err)
		}

		return nil
	}
}

func scanInput(r io.Reader, w io.Writer, input chan<- rocketcode.PromptInput, root *os.Root, cwd string) error {
	defer close(input)

	if _, err := fmt.Fprint(w, "rocketcode> "); err != nil {
		return fmt.Errorf("print input prompt: %w", err)
	}

	for {
		var line strings.Builder

		for {
			var ch rune

			_, err := fmt.Fscanf(r, "%c", &ch)
			if errors.Is(err, io.EOF) {
				if line.Len() > 0 {
					prompt, err := promptInput(line.String(), root, cwd)
					if err != nil {
						return err
					}

					if err := sendPromptInput(w, input, prompt); err != nil {
						return err
					}
				}

				return nil
			}

			if err != nil {
				return fmt.Errorf("scan stdin: %w", err)
			}

			if ch == '\n' {
				break
			}

			line.WriteRune(ch)
		}

		text := line.String()
		switch strings.TrimSpace(text) {
		case "/exit", "/quit":
			return nil
		default:
			prompt, err := promptInput(text, root, cwd)
			if err != nil {
				return err
			}

			if err := sendPromptInput(w, input, prompt); err != nil {
				return err
			}
		}
	}
}

func sendPromptInput(w io.Writer, input chan<- rocketcode.PromptInput, prompt rocketcode.PromptInput) error {
	responses := make(chan rocketcode.ChatResponse, 100)

	prompt.Responses = responses
	input <- prompt

	if err := printOutput(w, responses); err != nil {
		return err
	}

	if _, err := fmt.Fprint(w, "rocketcode> "); err != nil {
		return fmt.Errorf("print input prompt: %w", err)
	}

	return nil
}

func promptInput(text string, root *os.Root, cwd string) (rocketcode.PromptInput, error) {
	role := rocketcode.PromptInputRoleUser

	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if rest, ok := strings.CutPrefix(text, "developer:"); ok {
		role = rocketcode.PromptInputRoleDeveloper
		text = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}

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

	return rocketcode.PromptInput{Role: role, Text: text, Attachments: attachments}, nil
}

func printOutput(w io.Writer, output <-chan rocketcode.ChatResponse) error {
	for item := range output {
		line := item.Text
		switch item.Kind {
		case rocketcode.ChatResponseAssistantCommentary:
			line = "[assistant commentary] " + item.Text
		case rocketcode.ChatResponseAssistantTool:
			payload, err := json.Marshal(struct {
				Tool     *rocketcode.ToolDiagnostic     `json:"tool,omitempty"`
				Subagent *rocketcode.SubagentDiagnostic `json:"subagent,omitempty"`
				Provider *rocketcode.ProviderDiagnostic `json:"provider,omitempty"`
			}{
				Tool:     item.Tool,
				Subagent: item.Subagent,
				Provider: item.Provider,
			})
			if err != nil {
				return fmt.Errorf("marshal assistant tool response: %w", err)
			}

			line = "[assistant tool] " + string(payload)
		case rocketcode.ChatResponseReasoningSummary:
			line = "[reasoning summary] " + item.Text
		case rocketcode.ChatResponseAssistantMessage:
			line = "[assistant message] " + item.Text
		}

		if _, err := fmt.Fprintln(w, line); err != nil {
			return fmt.Errorf("print chat response: %w", err)
		}
	}

	return nil
}
