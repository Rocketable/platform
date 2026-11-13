package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"gopkg.in/yaml.v3"
)

// AgentDraftOpKind names a change to an agent draft.
type AgentDraftOpKind string

// The changes to an agent draft. An edit of the whole file and a restore are requests: the server commits
// them as the other kinds.
const (
	AgentDraftCreate         AgentDraftOpKind = "create"
	AgentDraftSetField       AgentDraftOpKind = "set_field"
	AgentDraftEditBody       AgentDraftOpKind = "edit_body"
	AgentDraftAddVariant     AgentDraftOpKind = "add_variant"
	AgentDraftRenameVariant  AgentDraftOpKind = "rename_variant"
	AgentDraftRemoveVariant  AgentDraftOpKind = "remove_variant"
	AgentDraftSetMode        AgentDraftOpKind = "set_mode"
	AgentDraftSetExternalMCP AgentDraftOpKind = "set_external_mcp"
	AgentDraftSetTestChat    AgentDraftOpKind = "set_test_chat"
	AgentDraftSetSaveState   AgentDraftOpKind = "set_save_state"
	AgentDraftRename         AgentDraftOpKind = "rename"
	AgentDraftEditFile       AgentDraftOpKind = "edit_file"
	AgentDraftRestore        AgentDraftOpKind = "restore"
)

// AgentDraftSaveState is where a draft is in saving.
type AgentDraftSaveState string

// The save states of an agent draft.
const (
	AgentDraftEditing AgentDraftSaveState = "editing"
	AgentDraftSaving  AgentDraftSaveState = "saving"
	AgentDraftWaiting AgentDraftSaveState = "waiting"
	AgentDraftLive    AgentDraftSaveState = "live"
)

// agentDraftWorkspaceLayer names workspace agents/ among a draft's save destinations.
const agentDraftWorkspaceLayer = "workspace"

// The metadata of a draft test message: the agent files it was sent with, by their path under agents/, as
// JSON, and the live agent's file then, absent when there is none.
const (
	agentDraftFilesMetadataKey = "rocketclaw_agent_draft_files"
	agentDraftLiveMetadataKey  = "rocketclaw_agent_draft_live"
)

// An agent name is one path segment that Git and the shell cannot read as an option or a hidden file.
var agentDraftNamePattern = regexp.MustCompile(`\A[A-Za-z0-9_][A-Za-z0-9._-]*\z`)

// agentDraftFilePattern splits an agent file as the agent loader does.
var agentDraftFilePattern = regexp.MustCompile(`\A---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|\z)`)

var agentDraftYAMLLinePattern = regexp.MustCompile(`line (\d+)`)

//go:embed agent_templates/*.md
var agentDraftTemplates embed.FS

// AgentDraft is an agent being written in the Web editor. Its first variant is the agent itself and the
// others are its comparison shadows; all share the draft's mode.
type AgentDraft struct {
	ID          string                `json:"-"`
	Revision    int64                 `json:"-"`
	Name        string                `json:"name"`
	Mode        harness.AgentMode     `json:"mode"`
	Variants    []AgentDraftVariant   `json:"variants"`
	ExternalMCP AgentDraftExternalMCP `json:"externalMcp,omitzero"`
	// TestChat is the conversation the draft's test messages go to.
	TestChat  string              `json:"testChat,omitempty"`
	SaveState AgentDraftSaveState `json:"saveState"`
	// Bases holds each save destination's file for Name, nil for none, as of when the draft took the name:
	// workspace agents/ as "workspace" and each configured Git overlay by its spec.
	Bases map[string]*string `json:"bases"`
	// Pending is the draft's last save to a Git overlay, kept after it stops waiting for its merge.
	Pending *AgentDraftPending `json:"pending,omitempty"`
}

// AgentDraftVariant is one version of a draft's agent file: its settings other than mode, and its instructions.
type AgentDraftVariant struct {
	Name     string              `json:"name"`
	Settings []AgentDraftSetting `json:"settings"`
	Body     string              `json:"body"`
}

// AgentDraftSetting is one top-level setting of an agent file; Value is its YAML.
type AgentDraftSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// AgentDraftSplice removes Remove code points at At, then inserts Insert there.
type AgentDraftSplice struct {
	At     int    `json:"at"`
	Remove int    `json:"remove,omitzero"`
	Insert string `json:"insert,omitzero"`
}

// AgentDraftExternalMCP is how a draft's test chat poses as an External MCP call.
type AgentDraftExternalMCP struct {
	On       bool              `json:"on,omitempty"`
	Caller   string            `json:"caller,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AgentDraftOp is one change to a draft, made by Author on revision BaseRevision. OpID, the client's id and a
// sequence number, lets a client recognize its own change among the committed ones.
type AgentDraftOp struct {
	// Revision and At say when the change committed.
	Revision     int64            `json:"revision,omitempty"`
	At           time.Time        `json:"at,omitzero"`
	BaseRevision int64            `json:"baseRevision"`
	OpID         string           `json:"opId"`
	Author       string           `json:"author"`
	Kind         AgentDraftOpKind `json:"kind"`
	// Variant names the variant a setting, instructions or variant change applies to.
	Variant string `json:"variant,omitempty"`
	Key     string `json:"key,omitempty"`
	// Value is a setting's YAML, empty to remove it; a variant's new name, or for a new variant the one it
	// copies; the mode, test chat, save state or agent name; or the whole file of an edit in the file.
	Value       string                `json:"value,omitempty"`
	Splices     []AgentDraftSplice    `json:"splices,omitempty"`
	ExternalMCP AgentDraftExternalMCP `json:"externalMcp,omitzero"`
	// From is the revision a restore brings back.
	From int64 `json:"from,omitempty"`
	// Bases are the save destinations' files for a new agent name, which the server reads.
	Bases map[string]*string `json:"bases,omitempty"`
}

// AgentDraftOrigin is where a new draft starts: blank in Mode, from the starter agent Template, or, with Copy,
// as a copy of the effective agent Name.
type AgentDraftOrigin struct {
	Name     string
	Mode     harness.AgentMode
	Template string
	Copy     bool
}

// AgentDraftPresence is where an open editor is in a draft: its variant, and its place, a setting key or an
// offset in the instructions.
type AgentDraftPresence struct {
	Client, Username, Variant, Place string
}

// AgentDraftProblem is why a draft's variant would not load, by the setting at fault.
type AgentDraftProblem struct {
	Variant, Key, Sentence string
}

// AgentDraftNameError reports a name that cannot be an agent's or a variant's.
type AgentDraftNameError struct{ Name string }

func (e *AgentDraftNameError) Error() string {
	return fmt.Sprintf("%q can't be a name: use only letters, digits, '.', '_' and '-', and don't start with '.' or '-'.", e.Name)
}

// AgentDraftNotValidError reports agent file text, or a setting's YAML, that does not parse yet.
type AgentDraftNotValidError struct{ Line int }

func (e *AgentDraftNotValidError) Error() string {
	return fmt.Sprintf("line %d isn't valid yet", e.Line)
}

// AgentDraftOpError reports a change a draft cannot take.
type AgentDraftOpError struct{ Problem string }

func (e *AgentDraftOpError) Error() string { return e.Problem }

// AgentDraftTemplates names the starter agents a draft can begin from.
func AgentDraftTemplates() []string {
	entries, _ := agentDraftTemplates.ReadDir("agent_templates") // The embedded folder always exists.
	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}

	return names
}

// CreateAgentDraft starts a draft at revision 1 with one variant, "a", and records the save destinations'
// files for its name.
func (s *SessionService) CreateAgentDraft(ctx context.Context, author string, origin AgentDraftOrigin) (*AgentDraft, error) {
	if !agentDraftNamePattern.MatchString(origin.Name) {
		return nil, &AgentDraftNameError{Name: origin.Name}
	}

	effective, bases, err := s.agentLayerFiles(origin.Name)
	if err != nil {
		return nil, err
	}

	draft := &AgentDraft{ID: rand.Text(), Revision: 1, Name: origin.Name, Mode: origin.Mode, Variants: []AgentDraftVariant{{Name: "a"}}, SaveState: AgentDraftEditing, Bases: bases}

	var text []byte

	switch {
	case origin.Template != "":
		if text, err = agentDraftTemplates.ReadFile("agent_templates/" + origin.Template + ".md"); err != nil {
			return nil, fmt.Errorf("read starter agent %q: %w", origin.Template, err)
		}
	case origin.Copy && effective == nil:
		return nil, fmt.Errorf("copy agent %q: %w", origin.Name, fs.ErrNotExist)
	case origin.Copy:
		text = []byte(*effective)
	}

	if text != nil {
		if draft.Mode, draft.Variants[0], err = parseAgentDraftFile(string(text)); err != nil {
			return nil, fmt.Errorf("parse agent %q: %w", origin.Name, err)
		}

		draft.Variants[0].Name = "a"
	}

	if !slices.Contains([]harness.AgentMode{harness.AgentModeEasy, harness.AgentModeAdvanced}, draft.Mode) {
		return nil, &AgentDraftOpError{Problem: "An agent is easy or advanced."}
	}

	doc, _ := json.Marshal(draft) // Strings, maps and slices of them always encode.
	op, _ := json.Marshal(AgentDraftOp{Author: author, Kind: AgentDraftCreate})

	_, err = execState(ctx, s.db, "create agent draft", `WITH draft AS (
    INSERT INTO agent_drafts (id, revision, draft, created_by) VALUES ($1, 1, $2, $3) RETURNING id
)
INSERT INTO agent_draft_ops (draft_id, revision, op, draft) SELECT id, 1, $4, $2 FROM draft`, draft.ID, string(doc), author, string(op))
	if err != nil {
		return nil, err
	}

	return draft, nil
}

// LoadAgentDraft returns the draft id at its current revision.
func (s *SessionService) LoadAgentDraft(ctx context.Context, id string) (*AgentDraft, error) {
	draft, err := scanAgentDraft(s.db.QueryRowContext(ctx, `SELECT id, revision, draft FROM agent_drafts WHERE id = $1`, id))

	return &draft, err
}

// CaptureAgentDraft records on inbound, a message to draft id's test chat, the draft's agent files as they
// stand now and the live agent's effective file, and makes inbound run as the draft's agent. Every column
// answering inbound runs these files, never what the draft or the disk holds when it runs. It returns the
// draft and the live file it captured, nil when there is none.
func (s *SessionService) CaptureAgentDraft(ctx context.Context, id string, inbound *protocol.InboundMessage) (*AgentDraft, *string, error) {
	draft, err := s.LoadAgentDraft(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	live, _, err := s.agentLayerFiles(draft.Name)
	if err != nil {
		return nil, nil, err
	}

	files := map[string]string{}

	for i := range draft.Variants {
		file := path.Join(draft.Name, draft.Variants[i].Name+".md")
		if i == 0 {
			file = draft.Name + ".md"
		}

		files[file] = string(renderAgentDraftFile(draft.Mode, &draft.Variants[i]))
	}

	data, _ := json.Marshal(files) // Strings always encode.
	inbound.Metadata[agentDraftFilesMetadataKey], inbound.Metadata[inboundAgentMetadataKey] = string(data), draft.Name

	if live != nil {
		inbound.Metadata[agentDraftLiveMetadataKey] = *live
	}

	return draft, live, nil
}

// ListAgentDrafts returns every draft, newest first.
func (s *SessionService) ListAgentDrafts(ctx context.Context) ([]AgentDraft, error) {
	return queryRows(ctx, s.db, `SELECT id, revision, draft FROM agent_drafts ORDER BY created_at DESC, id`, "agent drafts", scanAgentDraft)
}

// AgentDraftOps returns the changes draft id committed after revision since, in order.
func (s *SessionService) AgentDraftOps(ctx context.Context, id string, since int64) ([]AgentDraftOp, error) {
	return queryRows(ctx, s.db, `SELECT revision, at, op FROM agent_draft_ops WHERE draft_id = $1 AND revision > $2 ORDER BY revision`, "agent draft ops", func(row rowScanner) (AgentDraftOp, error) {
		var (
			op       AgentDraftOp
			revision int64
			at       time.Time
			raw      []byte
		)

		err := row.Scan(&revision, &at, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &op)
		}

		if err != nil {
			return op, fmt.Errorf("read agent draft op: %w", err)
		}

		op.Revision, op.At = revision, at

		return op, nil
	}, id, since)
}

// ApplyAgentDraftOp commits op as draft id's next revisions and wakes the stream "draft:<id>". An op made
// before others committed is moved past them: settings take the last write, and instructions keep both
// people's text. An edit of the whole file commits the changed settings, the mode, and one instructions
// change found by diffing against the file at op.BaseRevision; a restore commits what brings the mode,
// settings and instructions of revision op.From back. It returns the draft's new revision.
func (s *SessionService) ApplyAgentDraftOp(ctx context.Context, id string, op *AgentDraftOp) (int64, error) {
	for {
		draft, err := s.LoadAgentDraft(ctx, id)
		if err != nil {
			return 0, err
		}

		ops, err := s.agentDraftChanges(ctx, draft, op)
		if err != nil || len(ops) == 0 {
			return draft.Revision, err
		}

		revision, docs := draft.Revision, make([]json.RawMessage, 0, len(ops))
		for i := range ops {
			if err := applyAgentDraftOp(draft, &ops[i]); err != nil {
				return 0, err
			}

			doc, _ := json.Marshal(draft) // Strings, maps and slices of them always encode.
			docs = append(docs, doc)
		}

		opsJSON, _ := json.Marshal(ops)
		docsJSON, _ := json.Marshal(docs)

		// The update matches nothing when another change committed since the draft was read; then try again.
		committed, err := execRows(ctx, s.db, "commit agent draft op", `WITH changed AS (
    UPDATE agent_drafts SET revision = $2 + jsonb_array_length($3), draft = $4 WHERE id = $1 AND revision = $2 RETURNING id
), logged AS (
    INSERT INTO agent_draft_ops (draft_id, revision, op, draft)
    SELECT c.id, $2 + o.n, o.op, o.draft FROM changed c CROSS JOIN ROWS FROM (jsonb_array_elements($3), jsonb_array_elements($5)) WITH ORDINALITY AS o(op, draft, n)
)
SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', 'draft:' || id, 'revision', pg_current_xact_id()::text)::text) FROM changed`,
			id, revision, string(opsJSON), string(docs[len(docs)-1]), string(docsJSON))
		if err != nil {
			return 0, err
		}

		if committed > 0 {
			return revision + int64(len(ops)), nil
		}
	}
}

// SetAgentDraftPresence records where presence's client is in draft id, forgets the other editors not seen
// for 30 seconds, and wakes the stream "draft:<id>".
func (s *SessionService) SetAgentDraftPresence(ctx context.Context, id string, presence AgentDraftPresence) error {
	_, err := execState(ctx, s.db, "record agent draft presence", `WITH gone AS (
    DELETE FROM agent_draft_presence WHERE draft_id = $1 AND client <> $2 AND seen_at < clock_timestamp() - interval '30 seconds'
), seen AS (
    INSERT INTO agent_draft_presence (draft_id, client, username, variant, place) VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (draft_id, client) DO UPDATE SET username = excluded.username, variant = excluded.variant, place = excluded.place, seen_at = excluded.seen_at
    RETURNING draft_id
)
SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', 'draft:' || draft_id, 'revision', pg_current_xact_id()::text)::text) FROM seen`,
		id, presence.Client, presence.Username, presence.Variant, presence.Place)

	return err
}

// AgentDraftPresences returns the editors seen in draft id in the last 30 seconds, by client.
func (s *SessionService) AgentDraftPresences(ctx context.Context, id string) ([]AgentDraftPresence, error) {
	return queryRows(ctx, s.db, `SELECT client, username, variant, place FROM agent_draft_presence
WHERE draft_id = $1 AND seen_at >= clock_timestamp() - interval '30 seconds' ORDER BY client`, "agent draft presence", func(row rowScanner) (AgentDraftPresence, error) {
		var presence AgentDraftPresence
		if err := row.Scan(&presence.Client, &presence.Username, &presence.Variant, &presence.Place); err != nil {
			return presence, fmt.Errorf("read agent draft presence: %w", err)
		}

		return presence, nil
	}, id)
}

// ValidateAgentDraft loads each variant's file as the agent loader loads a saved one, and checks an easy
// agent's folder when it names no variables. For each variant that fails it returns the setting at fault
// and the setting help's sentence for what that setting allows.
func (s *SessionService) ValidateAgentDraft(draft *AgentDraft) ([]AgentDraftProblem, error) {
	var problems []AgentDraftProblem

	if err := s.definitions.Do(func(cfg *config.Config) error {
		root, err := os.OpenRoot(cfg.Workspace)
		if err != nil {
			return fmt.Errorf("open workspace root: %w", err)
		}

		defer func() { _ = root.Close() }()

		skills, err := fs.Sub(root.FS(), path.Join(filepath.ToSlash(cfg.RuntimeDirName()), "skills"))
		if err != nil {
			return fmt.Errorf("open skills dir: %w", err)
		}

		for i := range draft.Variants {
			variant, file := &draft.Variants[i], draft.Name+".md"

			agents, err := loadAgentDefinitions(fstest.MapFS{file: {Data: renderAgentDraftFile(draft.Mode, variant)}}, skills, cfg)
			agent := agents.Items[draft.Name]

			// The checks definitions get after loading, as reading the runtime definitions runs them.
			tagsKey := "permission.rocketclaw." + setTagToolName
			if agent.Mode == harness.AgentModeEasy {
				tagsKey = "tags"
			}

			if err == nil {
				if _, errTags := agentTagGroups(&agent); errTags != nil {
					err = fmt.Errorf("%s: %w", tagsKey, errTags)
				} else if _, _, errRecipients := agentRecipients(&agent); errRecipients != nil {
					err = fmt.Errorf("permission.rocketclaw.%s: %w", recipientSetting, errRecipients)
				}
			}

			if err == nil && agent.Mode == harness.AgentModeEasy && !strings.Contains(agent.WorkingDirectory, "$") {
				_, err = resolveWorkingDirectory(cfg, agent.WorkingDirectory, nil)
			}

			if err != nil {
				problems = append(problems, agentDraftProblem(variant.Name, draft.Mode, strings.TrimPrefix(err.Error(), file+": ")))
			}
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("validate agent draft: %w", err)
	}

	return problems, nil
}

// agentDraftChanges returns the ops op commits on draft, each moved past the ops committed after its base.
func (s *SessionService) agentDraftChanges(ctx context.Context, draft *AgentDraft, op *AgentDraftOp) ([]AgentDraftOp, error) {
	if op.BaseRevision < 1 || op.BaseRevision > draft.Revision {
		return nil, &AgentDraftOpError{Problem: "The change was made on a version this draft does not have."}
	}

	ops := []AgentDraftOp{*op}

	switch op.Kind {
	case AgentDraftEditFile:
		base, err := s.agentDraftAt(ctx, draft.ID, op.BaseRevision)
		if err != nil {
			return nil, err
		}

		mode, edited, err := parseAgentDraftFile(op.Value)
		if err != nil {
			return nil, err
		}

		i := slices.IndexFunc(base.Variants, func(variant AgentDraftVariant) bool { return variant.Name == op.Variant })
		if i < 0 {
			return nil, &AgentDraftOpError{Problem: "That variant does not exist."}
		}

		ops = diffAgentDraft(op, base.Mode, mode, &base.Variants[i], &edited)
	case AgentDraftRestore:
		old, err := s.agentDraftAt(ctx, draft.ID, op.From)
		if err != nil {
			return nil, err
		}

		// Restored values replace the current ones, so they need no moving past later ops. The mode changes
		// once, with the first variant both revisions have.
		current, mode := *op, draft.Mode
		current.BaseRevision, ops = draft.Revision, nil

		for _, variant := range draft.Variants {
			if j := slices.IndexFunc(old.Variants, func(v AgentDraftVariant) bool { return v.Name == variant.Name }); j >= 0 {
				current.Variant = variant.Name
				ops = append(ops, diffAgentDraft(&current, mode, old.Mode, &variant, &old.Variants[j])...)
				mode = old.Mode
			}
		}
	case AgentDraftRename:
		if !agentDraftNamePattern.MatchString(op.Value) {
			return nil, &AgentDraftNameError{Name: op.Value}
		}

		var err error
		if _, ops[0].Bases, err = s.agentLayerFiles(op.Value); err != nil {
			return nil, err
		}
	case AgentDraftCreate, AgentDraftSetField, AgentDraftEditBody, AgentDraftAddVariant, AgentDraftRenameVariant, AgentDraftRemoveVariant,
		AgentDraftSetMode, AgentDraftSetExternalMCP, AgentDraftSetTestChat, AgentDraftSetSaveState:
	}

	later, err := s.AgentDraftOps(ctx, draft.ID, op.BaseRevision)
	if err != nil {
		return nil, err
	}

	for i := range ops {
		for j := range later {
			if committed := &later[j]; committed.Revision > ops[i].BaseRevision && committed.Variant == ops[i].Variant && ops[i].Variant != "" {
				switch {
				case committed.Kind == AgentDraftRenameVariant:
					ops[i].Variant = committed.Value
				case committed.Kind == AgentDraftRemoveVariant:
					ops[i].Variant = ""
				case committed.Kind == AgentDraftEditBody && ops[i].Kind == AgentDraftEditBody:
					ops[i].Splices, _ = transformAgentDraftSplices(ops[i].Splices, committed.Splices, false)
				}
			}
		}
	}

	return ops, nil
}

// agentDraftAt returns draft id as revision left it.
func (s *SessionService) agentDraftAt(ctx context.Context, id string, revision int64) (*AgentDraft, error) {
	draft, err := scanAgentDraft(s.db.QueryRowContext(ctx, `SELECT draft_id, revision, draft FROM agent_draft_ops WHERE draft_id = $1 AND revision = $2`, id, revision))

	return &draft, err
}

// agentLayerFiles reads the effective agent file for name and each save destination's file for it, nil
// where there is none, under the configuration lock as definitions are read.
func (s *SessionService) agentLayerFiles(name string) (effective *string, bases map[string]*string, err error) {
	if err := s.definitions.Do(func(cfg *config.Config) error {
		root, err := os.OpenRoot(cfg.Workspace)
		if err != nil {
			return fmt.Errorf("open workspace root: %w", err)
		}

		defer func() { _ = root.Close() }()

		read := func(dir string) (*string, error) {
			data, err := root.ReadFile(filepath.Join(dir, "agents", name+".md"))
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}

			if err != nil {
				return nil, fmt.Errorf("read agent %q: %w", name, err)
			}

			return new(string(data)), nil
		}

		layers := map[string]string{agentDraftWorkspaceLayer: ""}
		for _, overlay := range skel.OverlayInfos("", cfg.RuntimeDirName(), cfg.Overlays) {
			layers[overlay.Spec] = overlay.ClonePath
		}

		bases = map[string]*string{}
		for layer, dir := range layers {
			if bases[layer], err = read(dir); err != nil {
				return err
			}
		}

		effective, err = read(cfg.RuntimeDirName())

		return err
	}); err != nil {
		return nil, nil, fmt.Errorf("read agent %q files: %w", name, err)
	}

	return effective, bases, nil
}

// applyAgentDraftOp changes draft as op says.
func applyAgentDraftOp(draft *AgentDraft, op *AgentDraftOp) error {
	switch op.Kind {
	case AgentDraftSetField, AgentDraftEditBody, AgentDraftAddVariant, AgentDraftRenameVariant, AgentDraftRemoveVariant:
		return applyAgentDraftVariantOp(draft, op)
	case AgentDraftSetMode:
		mode := harness.AgentMode(op.Value)
		if !slices.Contains([]harness.AgentMode{harness.AgentModeEasy, harness.AgentModeAdvanced}, mode) {
			return &AgentDraftOpError{Problem: "An agent is easy or advanced."}
		}

		// A setting the help lists for the other mode only goes, as does, for easy, one it does not list.
		draft.Mode = mode
		for i := range draft.Variants {
			draft.Variants[i].Settings = slices.DeleteFunc(draft.Variants[i].Settings, func(setting AgentDraftSetting) bool {
				help, listed := agentSettingHelp(setting.Key, mode)

				return help == nil && (listed || mode == harness.AgentModeEasy)
			})
		}
	case AgentDraftSetExternalMCP:
		draft.ExternalMCP = op.ExternalMCP
	case AgentDraftSetTestChat:
		draft.TestChat = op.Value
	case AgentDraftSetSaveState:
		state := AgentDraftSaveState(op.Value)
		if !slices.Contains([]AgentDraftSaveState{AgentDraftEditing, AgentDraftSaving, AgentDraftWaiting, AgentDraftLive}, state) {
			return &AgentDraftOpError{Problem: "That is not a save state."}
		}

		draft.SaveState = state
	case AgentDraftRename:
		draft.Name, draft.Bases = op.Value, op.Bases
	case AgentDraftCreate, AgentDraftEditFile, AgentDraftRestore:
		return &AgentDraftOpError{Problem: "That change cannot be made to a draft."}
	default:
		return &AgentDraftOpError{Problem: "That change cannot be made to a draft."}
	}

	return nil
}

// applyAgentDraftVariantOp makes a setting, instructions or variant change. A setting or instructions change
// to a variant removed since it was made changes nothing.
func applyAgentDraftVariantOp(draft *AgentDraft, op *AgentDraftOp) error {
	named := func(name string) func(AgentDraftVariant) bool {
		return func(variant AgentDraftVariant) bool { return variant.Name == name }
	}
	i := slices.IndexFunc(draft.Variants, named(op.Variant))

	if op.Kind == AgentDraftAddVariant || op.Kind == AgentDraftRenameVariant {
		// A new variant copies the one Value names; a renamed one takes Value as its name.
		name, from := op.Value, i
		if op.Kind == AgentDraftAddVariant {
			name, from = op.Variant, slices.IndexFunc(draft.Variants, named(op.Value))
		}

		switch {
		case from < 0:
			return &AgentDraftOpError{Problem: "That variant does not exist."}
		case !agentDraftNamePattern.MatchString(name):
			return &AgentDraftNameError{Name: name}
		case slices.ContainsFunc(draft.Variants, named(name)):
			return &AgentDraftOpError{Problem: "A variant with that name exists."}
		case op.Kind == AgentDraftRenameVariant:
			draft.Variants[i].Name = name
		default:
			source := draft.Variants[from]
			draft.Variants = append(draft.Variants, AgentDraftVariant{Name: name, Settings: slices.Clone(source.Settings), Body: source.Body})
		}

		return nil
	}

	if op.Kind == AgentDraftSetField && (op.Key == "" || op.Key == "mode") {
		return &AgentDraftOpError{Problem: "Change the agent type with the mode."}
	}

	switch {
	case i < 0:
	case op.Kind == AgentDraftRemoveVariant && len(draft.Variants) == 1:
		return &AgentDraftOpError{Problem: "A draft keeps at least one variant."}
	case op.Kind == AgentDraftRemoveVariant:
		draft.Variants = slices.Delete(draft.Variants, i, i+1)
	case op.Kind == AgentDraftEditBody:
		text := []rune(draft.Variants[i].Body)
		for _, splice := range op.Splices {
			if splice.At < 0 || splice.Remove < 0 || splice.At+splice.Remove > len(text) {
				return &AgentDraftOpError{Problem: "The change is outside the instructions."}
			}

			text = slices.Concat(text[:splice.At], []rune(splice.Insert), text[splice.At+splice.Remove:])
		}

		draft.Variants[i].Body = string(text)
	default:
		variant := &draft.Variants[i]
		j := slices.IndexFunc(variant.Settings, func(setting AgentDraftSetting) bool { return setting.Key == op.Key })

		var value yaml.Node
		if op.Value != "" {
			if err := yaml.Unmarshal([]byte(op.Value), &value); err != nil || len(value.Content) == 0 {
				return &AgentDraftNotValidError{Line: agentDraftYAMLLine(err)}
			}
		}

		switch {
		case op.Value == "" && j >= 0:
			variant.Settings = slices.Delete(variant.Settings, j, j+1)
		case op.Value == "":
		case j < 0:
			variant.Settings = append(variant.Settings, AgentDraftSetting{Key: op.Key, Value: encodeAgentDraftYAML(value.Content[0])})
		default:
			variant.Settings[j].Value = encodeAgentDraftYAML(value.Content[0])
		}
	}

	return nil
}

// diffAgentDraft returns the ops, made like op, that turn variant from in fromMode into to in toMode: a mode
// change first, then a change for each setting that differs, then one instructions change.
func diffAgentDraft(op *AgentDraftOp, fromMode, toMode harness.AgentMode, from, to *AgentDraftVariant) []AgentDraftOp {
	var ops []AgentDraftOp

	change := func(kind AgentDraftOpKind) *AgentDraftOp {
		ops = append(ops, AgentDraftOp{BaseRevision: op.BaseRevision, OpID: op.OpID, Author: op.Author, Kind: kind, Variant: op.Variant, From: op.From})

		return &ops[len(ops)-1]
	}

	if fromMode != toMode {
		change(AgentDraftSetMode).Value = string(toMode)
	}

	for _, setting := range to.Settings {
		if !slices.Contains(from.Settings, setting) {
			set := change(AgentDraftSetField)
			set.Key, set.Value = setting.Key, setting.Value
		}
	}

	for _, setting := range from.Settings {
		if !slices.ContainsFunc(to.Settings, func(kept AgentDraftSetting) bool { return kept.Key == setting.Key }) {
			change(AgentDraftSetField).Key = setting.Key
		}
	}

	if splices := diffAgentDraftBody(from.Body, to.Body); len(splices) > 0 {
		change(AgentDraftEditBody).Splices = splices
	}

	return ops
}

// diffAgentDraftBody returns splices, applied in order, that turn from into to with the fewest code points
// removed and inserted, so text nobody touched is never removed and typed again.
func diffAgentDraftBody(from, to string) []AgentDraftSplice {
	a, b := []rune(from), []rune(to)
	prefix, suffix := 0, 0

	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}

	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	a, b = a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]

	edits, ok := shortestAgentDraftEdits(a, b)
	if !ok {
		return []AgentDraftSplice{{At: prefix, Remove: len(a), Insert: string(b)}}
	}

	var splices []AgentDraftSplice

	shift := prefix
	for _, e := range edits {
		at := e.at + shift
		last := len(splices) - 1
		open := last >= 0 && splices[last].At+utf8.RuneCountInString(splices[last].Insert) == at

		switch {
		case e.insert < 0 && open:
			splices[last].Remove++
		case e.insert < 0:
			splices = append(splices, AgentDraftSplice{At: at, Remove: 1})
		case open:
			splices[last].Insert += string(e.insert)
		default:
			splices = append(splices, AgentDraftSplice{At: at, Insert: string(e.insert)})
		}

		if e.insert < 0 {
			shift--
		} else {
			shift++
		}
	}

	return splices
}

// agentDraftEdit inserts a code point before a's code point at, or with insert -1 removes that code point.
type agentDraftEdit struct {
	at     int
	insert rune
}

// shortestAgentDraftEdits returns, in order, the fewest edits that turn a into b (Myers' diff), or false past
// 2,000 edits.
func shortestAgentDraftEdits(a, b []rune) ([]agentDraftEdit, bool) {
	n, m := len(a), len(b)
	offset, trace := n+m, [][]int(nil)
	v := make([]int, 2*offset+2)

	// ponytail: the trace takes memory quadratic in the edits, so past 2,000 of them the caller replaces the
	// changed middle whole, which can undo a teammate's concurrent edit inside it; a linear-space Myers lifts this.
	for d := 0; d <= 2000; d++ {
		// trace[d] holds diagonals -d..d as step d found them, at index k+d.
		trace = append(trace, slices.Clone(v[offset-d:offset+d+1]))

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[offset+k-1] < v[offset+k+1] {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}

			for y := x - k; x < n && y < m && a[x] == b[y]; y++ {
				x++
			}

			v[offset+k] = x
			if x < n || x-k < m {
				continue
			}

			// Walk back from the end: each step left one diagonal and made one edit.
			edits := make([]agentDraftEdit, d)

			for x, y := n, m; d > 0; d-- {
				previous, k := trace[d], x-y
				prevK := k - 1

				if k == -d || k != d && previous[k-1+d] < previous[k+1+d] {
					prevK = k + 1
				}

				x = previous[prevK+d]
				y = x - prevK

				edits[d-1] = agentDraftEdit{at: x, insert: -1}
				if prevK == k+1 {
					edits[d-1].insert = b[y]
				}
			}

			return edits, true
		}
	}

	return nil, false
}

// transformAgentDraftSplices returns a moved past b and b moved past a, both made on the same text, by the
// co-editing model's rules: inserts at one spot keep server order (aFirst: a came first), text typed inside
// a deleted passage stays where the passage was, a delete splits around an insert inside it, and overlapping
// deletes drop the overlap. The results are splices that each remove or insert.
func transformAgentDraftSplices(a, b []AgentDraftSplice, aFirst bool) (aPast, bPast []AgentDraftSplice) {
	// Each splice is its remove, then its insert.
	var primitive [2][]AgentDraftSplice

	for i, splices := range [][]AgentDraftSplice{a, b} {
		for _, splice := range splices {
			if splice.Remove > 0 {
				primitive[i] = append(primitive[i], AgentDraftSplice{At: splice.At, Remove: splice.Remove})
			}

			if splice.Insert != "" {
				primitive[i] = append(primitive[i], AgentDraftSplice{At: splice.At, Insert: splice.Insert})
			}
		}
	}

	a, b = primitive[0], primitive[1]

	switch {
	case len(a) == 0 || len(b) == 0:
		return a, b
	case len(a) > 1:
		a1, b1 := transformAgentDraftSplices(a[:1], b, aFirst)
		a2, b2 := transformAgentDraftSplices(a[1:], b1, aFirst)

		return slices.Concat(a1, a2), b2
	case len(b) > 1:
		a1, b1 := transformAgentDraftSplices(a, b[:1], aFirst)
		a2, b2 := transformAgentDraftSplices(a1, b[1:], aFirst)

		return a2, slices.Concat(b1, b2)
	}

	return transformAgentDraftSplice(a[0], b[0], aFirst), transformAgentDraftSplice(b[0], a[0], !aFirst)
}

// transformAgentDraftSplice moves a past b, two splices that each remove or insert.
func transformAgentDraftSplice(a, b AgentDraftSplice, aFirst bool) []AgentDraftSplice {
	inserted := utf8.RuneCountInString(b.Insert)
	removedBefore := max(0, min(a.At, b.At+b.Remove)-b.At)

	switch {
	case a.Insert != "" && b.Insert != "":
		if b.At < a.At || b.At == a.At && !aFirst {
			a.At += inserted
		}
	case a.Insert != "":
		a.At -= removedBefore
	case b.Insert != "" && b.At <= a.At:
		a.At += inserted
	case b.Insert != "" && b.At < a.At+a.Remove:
		before := b.At - a.At

		return []AgentDraftSplice{{At: a.At, Remove: before}, {At: a.At + inserted, Remove: a.Remove - before}}
	case b.Insert == "":
		overlap := max(0, min(a.At+a.Remove, b.At+b.Remove)-max(a.At, b.At))
		if overlap == a.Remove {
			return nil
		}

		a.At, a.Remove = a.At-removedBefore, a.Remove-overlap
	}

	return []AgentDraftSplice{a}
}

// renderAgentDraftFile returns variant's agent file: mode for an easy agent, the settings the help lists in
// its order, then the others in their own order, then the instructions.
func renderAgentDraftFile(mode harness.AgentMode, variant *AgentDraftVariant) []byte {
	rank, settingsHelp := map[string]int{}, harness.AgentSettings()
	for i := range settingsHelp {
		if key, _, _ := strings.Cut(settingsHelp[i].Key, "."); rank[key] == 0 {
			rank[key] = len(rank) + 1
		}
	}

	settings := slices.Clone(variant.Settings)
	slices.SortStableFunc(settings, func(a, b AgentDraftSetting) int {
		return cmp.Compare(cmp.Or(rank[a.Key], len(rank)+1), cmp.Or(rank[b.Key], len(rank)+1))
	})

	frontmatter := &yaml.Node{Kind: yaml.MappingNode}
	if mode == harness.AgentModeEasy {
		frontmatter.Content = append(frontmatter.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "mode"}, &yaml.Node{Kind: yaml.ScalarNode, Value: string(mode)})
	}

	for _, setting := range settings {
		var value yaml.Node

		_ = yaml.Unmarshal([]byte(setting.Value), &value) // Values are checked when they are written.
		frontmatter.Content = append(frontmatter.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: setting.Key}, value.Content[0])
	}

	return []byte("---\n" + encodeAgentDraftYAML(frontmatter) + "\n---\n" + variant.Body)
}

// parseAgentDraftFile reads an agent file into its mode and a variant of its other settings and its instructions.
func parseAgentDraftFile(text string) (harness.AgentMode, AgentDraftVariant, error) {
	match := agentDraftFilePattern.FindStringSubmatchIndex(text)
	if match == nil {
		return "", AgentDraftVariant{}, &AgentDraftNotValidError{Line: 1}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text[match[2]:match[3]]), &doc); err != nil {
		return "", AgentDraftVariant{}, &AgentDraftNotValidError{Line: agentDraftYAMLLine(err) + 1}
	}

	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", AgentDraftVariant{}, &AgentDraftNotValidError{Line: 2}
	}

	mode, variant := harness.AgentModeAdvanced, AgentDraftVariant{Body: text[match[1]:]}
	frontmatter := doc.Content[0].Content

	for i := 0; i+1 < len(frontmatter); i += 2 {
		key, value := frontmatter[i], frontmatter[i+1]

		switch {
		case key.Value == "mode" && !slices.Contains([]string{"easy", "advanced"}, value.Value),
			slices.ContainsFunc(variant.Settings, func(setting AgentDraftSetting) bool { return setting.Key == key.Value }):
			return "", AgentDraftVariant{}, &AgentDraftNotValidError{Line: key.Line + 1}
		case key.Value == "mode":
			mode = harness.AgentMode(value.Value)
		default:
			variant.Settings = append(variant.Settings, AgentDraftSetting{Key: key.Value, Value: encodeAgentDraftYAML(value)})
		}
	}

	return mode, variant, nil
}

// encodeAgentDraftYAML returns node's YAML, indented by two spaces, without the final line break.
func encodeAgentDraftYAML(node *yaml.Node) string {
	var out strings.Builder

	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	_ = encoder.Encode(node) // A node parsed from YAML, or built of them, always encodes.
	_ = encoder.Close()

	return strings.TrimSuffix(out.String(), "\n")
}

// agentDraftYAMLLine is the line a yaml.v3 error names, 1 when it names none.
func agentDraftYAMLLine(err error) int {
	if match := agentDraftYAMLLinePattern.FindStringSubmatch(fmt.Sprint(err)); match != nil {
		line, _ := strconv.Atoi(match[1]) // The pattern matches digits only.
		return line
	}

	return 1
}

// agentDraftProblem places a loader message, "key: problem", on the closest setting the help describes for
// mode, with the help's sentence for what it allows, never the loader's own words.
func agentDraftProblem(variant string, mode harness.AgentMode, message string) AgentDraftProblem {
	key, _, _ := strings.Cut(message, ": ")
	key, _, _ = strings.Cut(strings.TrimPrefix(key, "parse "), " ")

	for k := key; k != ""; k = k[:max(0, strings.LastIndex(k, "."))] {
		if help, _ := agentSettingHelp(k, mode); help != nil {
			return AgentDraftProblem{Variant: variant, Key: k, Sentence: help.Allowed}
		}
	}

	return AgentDraftProblem{Variant: variant, Key: key, Sentence: "This setting isn't valid here yet."}
}

// agentSettingHelp returns the setting help for key in mode, nil when there is none, and whether the help
// lists key for any mode.
func agentSettingHelp(key string, mode harness.AgentMode) (*harness.AgentSetting, bool) {
	var listed bool

	settings := harness.AgentSettings()
	for i := range settings {
		if settings[i].Key == key && slices.Contains(settings[i].Modes, mode) {
			return &settings[i], true
		}

		listed = listed || settings[i].Key == key
	}

	return nil, listed
}

// scanAgentDraft reads an id, a revision, and a draft's JSON.
func scanAgentDraft(row rowScanner) (AgentDraft, error) {
	var (
		draft AgentDraft
		raw   []byte
	)

	err := row.Scan(&draft.ID, &draft.Revision, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &draft)
	}

	if err != nil {
		return AgentDraft{}, fmt.Errorf("read agent draft: %w", err)
	}

	return draft, nil
}
