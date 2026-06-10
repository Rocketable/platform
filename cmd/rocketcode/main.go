// Command rocketcode starts RocketCode in CLI mode.
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
	_ "modernc.org/sqlite"
)

const defaultAgent = "main"
const maxAttachmentBytes = 5 * 1024 * 1024

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	config, err := configFromEnv()
	if err != nil {
		return err
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

	agentsRoot, agentsFS, err := fsFromRoot(root, "agents")
	if err != nil {
		return err
	}

	if agentsRoot != nil {
		defer func() { _ = agentsRoot.Close() }()
	}

	skillsRoot, skillsFS, err := fsFromRoot(root, "skills")
	if err != nil {
		return err
	}

	if skillsRoot != nil {
		defer func() { _ = skillsRoot.Close() }()
	}

	agents, skills := loadParsedAgentsAndSkills(agentsFS, skillsFS, skillsRootName(skillsRoot))

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

	looper, err := rocketcode.New(&client, config, root, agents, skills, defaultAgent, os.Stdout)
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
		ParallelToolCalls:          0,
		ShellOutputDir:             filepath.Join(".tmp", "shell-outputs"),
		SandboxedBash:              false,
		InterAgentFilter:           rocketcode.InterAgentFilterConfig{Prompt: "", Model: "", ReasoningEffort: "", Verbosity: "", Permission: rocketcode.PermissionSet{Buckets: nil}},
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
		fsys := child.FS()

		return child, fsys, nil
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

	return rocketcode.Attachment{
		MIME:     mimeType,
		Filename: filename,
		URL:      "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
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

	return rocketcode.PromptInput{Role: role, Text: strings.Join(kept, " "), Attachments: attachments, Responses: nil}, nil
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
