package quickbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/txtar"
)

const (
	metaMember     = "meta.txt"
	mocksMember    = "mocks/tools.json"
	criteriaMember = "elo/criteria.txt"
	judgeMember    = "elo/judge.txt"
	variationsRoot = "variations"
	systemName     = "system.txt"
	transcriptName = "transcript.json"
	modelName      = "model.txt"
)

// BAR is a loaded benchmark archive (directory or .bar).
type BAR struct {
	Path        string
	Meta        Meta
	Agents      map[string][]byte // agent name → full .md bytes (verbatim)
	Variations  []Variation
	Tools       []ToolMock
	BashDoubles []BashDouble
	Criteria    string
	Judge       string
	// members holds canonical path → content for pack/dump fidelity.
	members map[string][]byte
}

// Meta is BAR metadata from meta.txt.
type Meta struct {
	Name        string
	Description string
	Tags        []string
	Root        string // default agent name; empty means "main"
}

// Variation is one prompt variant under variations/<id>/.
type Variation struct {
	ID            string
	System        string // legacy: root prompt overlay when AgentOverlays[root].System empty
	Transcript    []Message
	AgentOverlays map[string]AgentOverlay
}

// Message is a user or assistant turn in a variation transcript.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// ToolMock is a static tool definition returned on every call.
type ToolMock struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Response    string         `json:"response"`
}

// Open loads and validates a BAR from a .bar file or unpacked directory.
func Open(path string) (*BAR, error) {
	path = filepath.Clean(path)

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		return nil, fmt.Errorf("%s: YAML benchmarks are not supported; use a .bar or BAR directory", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var members map[string][]byte
	if info.IsDir() {
		members, err = readDirMembers(path)
	} else {
		members, err = readArchiveMembers(path)
	}

	if err != nil {
		return nil, err
	}

	bar, err := barFromMembers(members)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	bar.Path = path

	return bar, nil
}

// Pack writes a BAR directory to a .bar txtar file.
func Pack(dir, outPath string) error {
	bar, err := Open(dir)
	if err != nil {
		return err
	}

	if err := checkTxtarSafe(bar.members); err != nil {
		return err
	}

	data := txtar.Format(&txtar.Archive{Files: membersToFiles(bar.members)})

	if parent := filepath.Dir(outPath); parent != "." && parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}

	return os.WriteFile(outPath, data, 0o644)
}

// txtar headers are lines of the form "-- name --". Member bodies must not contain them.
func checkTxtarSafe(members map[string][]byte) error {
	for name, data := range members {
		for i, line := range strings.Split(string(data), "\n") {
			if isTxtarHeader(line) {
				return fmt.Errorf("member %q line %d: body contains txtar header marker %q (cannot pack)", name, i+1, line)
			}
		}
	}

	return nil
}

func isTxtarHeader(line string) bool {
	if !strings.HasPrefix(line, "-- ") || !strings.HasSuffix(line, " --") {
		return false
	}

	return len(line) >= len("--  --")
}

// Unpack extracts a .bar archive to a directory.
func Unpack(barPath, outDir string) error {
	members, err := readArchiveMembers(barPath)
	if err != nil {
		return err
	}

	if _, err := barFromMembers(members); err != nil {
		return fmt.Errorf("%s: %w", barPath, err)
	}

	return writeMembers(outDir, members)
}

// WriteDir writes a BAR's members to an unpacked directory.
func WriteDir(dir string, bar *BAR) error {
	if bar.members == nil {
		members, err := membersFromBAR(bar)
		if err != nil {
			return err
		}

		bar.members = members
	}

	if _, err := barFromMembers(bar.members); err != nil {
		return err
	}

	return writeMembers(dir, bar.members)
}

// Dump writes BAR contents (or names only) to w.
func Dump(w io.Writer, bar *BAR, namesOnly bool) error {
	names := make([]string, 0, len(bar.members))
	for name := range bar.members {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		if _, err := fmt.Fprintln(w, name); err != nil {
			return err
		}

		if namesOnly {
			continue
		}

		if _, err := w.Write(bar.members[name]); err != nil {
			return err
		}

		if !bytes.HasSuffix(bar.members[name], []byte("\n")) {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}

		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	return nil
}

func readArchiveMembers(path string) (map[string][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	archive := txtar.Parse(data)

	members := make(map[string][]byte, len(archive.Files))
	for _, f := range archive.Files {
		name := filepath.ToSlash(f.Name)
		if !validMember(name) {
			return nil, fmt.Errorf("invalid BAR member %q", name)
		}

		members[name] = bytes.Clone(f.Data)
	}

	return members, nil
}

func readDirMembers(dir string) (map[string][]byte, error) {
	members := map[string][]byte{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		if rel == "." {
			return nil
		}

		name := filepath.ToSlash(rel)
		if d.IsDir() {
			if name == variationsRoot || name == "mocks" || name == "elo" || name == agentsRoot ||
				strings.HasPrefix(name, variationsRoot+"/") || strings.HasPrefix(name, agentsRoot+"/") {
				return nil
			}

			return fs.SkipDir
		}

		if !validMember(name) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		members[name] = data

		return nil
	})
	if err != nil {
		return nil, err
	}

	return members, nil
}

func validMember(name string) bool {
	name = filepath.ToSlash(name)
	switch {
	case name == metaMember, name == mocksMember, name == bashDoublesMember, name == criteriaMember, name == judgeMember:
		return true
	case strings.HasPrefix(name, agentsRoot+"/"):
		rest := strings.TrimPrefix(name, agentsRoot+"/")
		return rest != "" && !strings.Contains(rest, "/") && strings.HasSuffix(rest, ".md")
	case strings.HasPrefix(name, variationsRoot+"/"):
		rest := strings.TrimPrefix(name, variationsRoot+"/")

		parts := strings.Split(rest, "/")
		switch len(parts) {
		case 2:
			return parts[0] != "" && (parts[1] == systemName || parts[1] == transcriptName)
		case 4:
			// variations/<id>/agents/<name>/model.txt|system.txt
			return parts[0] != "" && parts[1] == agentsRoot && parts[2] != "" && (parts[3] == modelName || parts[3] == systemName)
		default:
			return false
		}
	default:
		return false
	}
}

func barFromMembers(members map[string][]byte) (*BAR, error) {
	if len(members) == 0 {
		return nil, errors.New("empty BAR")
	}

	meta, err := parseMeta(members[metaMember])
	if err != nil {
		return nil, err
	}

	criteria := string(members[criteriaMember])
	if strings.TrimSpace(criteria) == "" {
		return nil, errors.New("missing elo/criteria.txt")
	}

	judge := strings.TrimSpace(string(members[judgeMember]))
	if judge == "" {
		return nil, errors.New("missing elo/judge.txt")
	}

	var tools []ToolMock
	if data, ok := members[mocksMember]; ok {
		if err := json.Unmarshal(data, &tools); err != nil {
			return nil, fmt.Errorf("mocks/tools.json: %w", err)
		}
	}

	var bashDoubles []BashDouble

	if data, ok := members[bashDoublesMember]; ok {
		var err error

		bashDoubles, err = parseBashDoubles(data)
		if err != nil {
			return nil, err
		}
	}

	agents := map[string][]byte{}

	for name, data := range members {
		if !strings.HasPrefix(name, agentsRoot+"/") || !strings.HasSuffix(name, ".md") {
			continue
		}

		agentName := strings.TrimSuffix(strings.TrimPrefix(name, agentsRoot+"/"), ".md")
		if agentName == "" || strings.Contains(agentName, "/") {
			return nil, fmt.Errorf("invalid agent member %q", name)
		}

		agents[agentName] = data
	}

	if len(agents) == 0 {
		return nil, errors.New("at least one agents/<name>.md is required")
	}

	root := strings.TrimSpace(meta.Root)
	if root == "" {
		root = "main"
	}

	if _, ok := agents[root]; !ok {
		return nil, fmt.Errorf("root agent %q missing from agents/", root)
	}

	varIDs := map[string]struct{}{}

	for name := range members {
		if !strings.HasPrefix(name, variationsRoot+"/") {
			continue
		}

		parts := strings.Split(strings.TrimPrefix(name, variationsRoot+"/"), "/")
		if len(parts) >= 2 && parts[0] != "" {
			varIDs[parts[0]] = struct{}{}
		}
	}

	if len(varIDs) == 0 {
		return nil, errors.New("at least one variation is required")
	}

	ids := make([]string, 0, len(varIDs))
	for id := range varIDs {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	variations := make([]Variation, 0, len(ids))
	for _, id := range ids {
		base := variationsRoot + "/" + id + "/"

		transcriptData, ok := members[base+transcriptName]
		if !ok {
			return nil, fmt.Errorf("variation %q missing transcript.json", id)
		}

		var transcript []Message
		if err := json.Unmarshal(transcriptData, &transcript); err != nil {
			return nil, fmt.Errorf("variation %q transcript.json: %w", id, err)
		}

		if err := validateTranscript(transcript); err != nil {
			return nil, fmt.Errorf("variation %q: %w", id, err)
		}

		overlays := map[string]AgentOverlay{}

		prefix := base + agentsRoot + "/"
		for name, data := range members {
			if !strings.HasPrefix(name, prefix) {
				continue
			}

			rest := strings.TrimPrefix(name, prefix)

			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				continue
			}

			agentName, file := parts[0], parts[1]
			ov := overlays[agentName]

			switch file {
			case modelName:
				ov.Model = strings.TrimSpace(string(data))
			case systemName:
				ov.System = string(data)
			}

			overlays[agentName] = ov
		}

		for agentName := range overlays {
			if _, ok := agents[agentName]; !ok {
				return nil, fmt.Errorf("variation %q overlays unknown agent %q", id, agentName)
			}
		}

		variations = append(variations, Variation{
			ID:            id,
			System:        string(members[base+systemName]),
			Transcript:    transcript,
			AgentOverlays: overlays,
		})
	}

	return &BAR{
		Meta:        meta,
		Agents:      agents,
		Variations:  variations,
		Tools:       tools,
		BashDoubles: bashDoubles,
		Criteria:    criteria,
		Judge:       judge,
		members:     members,
	}, nil
}

func validateTranscript(messages []Message) error {
	if len(messages) == 0 {
		return errors.New("transcript is empty")
	}

	for i, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" {
			return fmt.Errorf("message %d: role must be user or assistant", i)
		}

		if strings.TrimSpace(m.Text) == "" {
			return fmt.Errorf("message %d: empty text", i)
		}
	}

	if messages[len(messages)-1].Role != "user" {
		return errors.New("final transcript message must be user")
	}

	return nil
}

func parseMeta(data []byte) (Meta, error) {
	meta := Meta{}
	if len(bytes.TrimSpace(data)) == 0 {
		return meta, nil
	}

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Meta{}, fmt.Errorf("meta.txt line %d: expected key: value", i+1)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "name":
			meta.Name = value
		case "description":
			meta.Description = value
		case "tags":
			for tag := range strings.SplitSeq(value, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					meta.Tags = append(meta.Tags, tag)
				}
			}
		case "root":
			meta.Root = value
		default:
			return Meta{}, fmt.Errorf("meta.txt unknown key %q", key)
		}
	}

	return meta, nil
}

func formatMeta(meta Meta) []byte {
	var b strings.Builder
	if meta.Name != "" {
		fmt.Fprintf(&b, "name: %s\n", meta.Name)
	}

	if meta.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", meta.Description)
	}

	if len(meta.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(meta.Tags, ", "))
	}

	root := strings.TrimSpace(meta.Root)
	if root == "" {
		root = "main"
	}

	fmt.Fprintf(&b, "root: %s\n", root)

	return []byte(b.String())
}

func membersFromBAR(bar *BAR) (map[string][]byte, error) {
	if len(bar.Agents) == 0 {
		return nil, errors.New("at least one agent is required")
	}

	members := map[string][]byte{
		metaMember:     formatMeta(bar.Meta),
		criteriaMember: []byte(bar.Criteria),
		judgeMember:    []byte(strings.TrimSpace(bar.Judge) + "\n"),
	}
	for name, data := range bar.Agents {
		members[agentsRoot+"/"+name+".md"] = ensureTrailingNewline(data)
	}

	if len(bar.Tools) > 0 {
		data, err := json.MarshalIndent(bar.Tools, "", "  ")
		if err != nil {
			return nil, err
		}

		members[mocksMember] = append(data, '\n')
	}

	if len(bar.BashDoubles) > 0 {
		data, err := json.MarshalIndent(bar.BashDoubles, "", "  ")
		if err != nil {
			return nil, err
		}

		members[bashDoublesMember] = append(data, '\n')
	}

	for _, v := range bar.Variations {
		base := variationsRoot + "/" + v.ID + "/"
		if strings.TrimSpace(v.System) != "" {
			members[base+systemName] = ensureTrailingNewline([]byte(v.System))
		}

		data, err := json.MarshalIndent(v.Transcript, "", "  ")
		if err != nil {
			return nil, err
		}

		members[base+transcriptName] = append(data, '\n')
		for agentName, overlay := range v.AgentOverlays {
			prefix := base + agentsRoot + "/" + agentName + "/"
			if m := strings.TrimSpace(overlay.Model); m != "" {
				members[prefix+modelName] = ensureTrailingNewline([]byte(m))
			}

			if s := strings.TrimSpace(overlay.System); s != "" {
				members[prefix+systemName] = ensureTrailingNewline([]byte(s))
			}
		}
	}

	return members, nil
}

func membersToFiles(members map[string][]byte) []txtar.File {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}

	slices.Sort(names)

	files := make([]txtar.File, 0, len(names))
	for _, name := range names {
		files = append(files, txtar.File{Name: name, Data: members[name]})
	}

	return files
}

func writeMembers(dir string, members map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for name, data := range members {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}

		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}

	return nil
}

func ensureTrailingNewline(data []byte) []byte {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data
	}

	return append(bytes.Clone(data), '\n')
}
