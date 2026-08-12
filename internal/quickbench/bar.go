package quickbench

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/txtar"
	"gopkg.in/yaml.v3"
)

const (
	benchMember    = "bench.yaml"
	variationsRoot = "variations"
	systemName     = "system.txt"
	turnsName      = "turns.yaml"
	modelName      = "model.txt"
)

// BAR is a loaded benchmark archive (directory or .bar).
type BAR struct {
	Path       string
	Meta       Meta
	Matrix     []MatrixEntry     // subject runs from bench.yaml; empty means one "default" cell
	Agents     map[string][]byte // agent name → full .md bytes (verbatim)
	Variations []Variation
	Criteria   string
	Judge      string
	// members holds canonical path → content for pack/dump fidelity.
	members map[string][]byte
}

// Meta is BAR metadata from bench.yaml.
type Meta struct {
	Name        string
	Description string
	Tags        []string
	Root        string // default agent name; empty means "main"
}

// MatrixEntry is one subject configuration in the run matrix (variation × matrix).
type MatrixEntry struct {
	ID     string
	Agents map[string]MatrixAgent // agent name → optional model/system overrides
}

// MatrixAgent overrides one agent for a matrix row. Zero fields keep the BAR agent value.
type MatrixAgent struct {
	Model  modelSelector // empty Raw means no model override
	System string        // empty means no system/prompt override
}

// Variation is one prompt variant under variations/<id>/.
type Variation struct {
	ID            string
	System        string // legacy: root prompt overlay when AgentOverlays[root].System empty
	Transcript    []Message
	AgentOverlays map[string]AgentOverlay
	Tools         []ToolMock
	BashDoubles   []BashDouble
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
	for _, name := range orderedMemberNames(bar.members) {
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
			if name == variationsRoot || name == "mocks" || name == agentsRoot ||
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
	case name == benchMember:
		return true
	case strings.HasPrefix(name, agentsRoot+"/"):
		rest := strings.TrimPrefix(name, agentsRoot+"/")
		return rest != "" && !strings.Contains(rest, "/") && strings.HasSuffix(rest, ".md")
	case strings.HasPrefix(name, variationsRoot+"/"):
		rest := strings.TrimPrefix(name, variationsRoot+"/")

		parts := strings.Split(rest, "/")
		switch len(parts) {
		case 2:
			return parts[0] != "" && (parts[1] == systemName || parts[1] == turnsName)
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

	meta, matrix, criteria, judge, err := parseBenchYAML(members[benchMember])
	if err != nil {
		return nil, err
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

		turnsData, ok := members[base+turnsName]
		if !ok {
			return nil, fmt.Errorf("variation %q missing turns.yaml", id)
		}

		transcript, tools, bashDoubles, err := parseTurnsYAML(turnsData)
		if err != nil {
			return nil, fmt.Errorf("variation %q: %w", id, err)
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
			Tools:         tools,
			BashDoubles:   bashDoubles,
		})
	}

	for _, entry := range matrix {
		for agentName := range entry.Agents {
			if _, ok := agents[agentName]; !ok {
				return nil, fmt.Errorf("bench.yaml: matrix %q references unknown agent %q", entry.ID, agentName)
			}
		}
	}

	return &BAR{
		Meta:       meta,
		Matrix:     matrix,
		Agents:     agents,
		Variations: variations,
		Criteria:   criteria,
		Judge:      judge,
		members:    members,
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

	hasUser := false
	for _, m := range messages {
		if m.Role == "user" {
			hasUser = true
			break
		}
	}

	if !hasUser {
		return errors.New("transcript has no user message")
	}

	return nil
}

// benchYAML is the single editable BAR config (metadata + matrix + ELO).
type benchYAML struct {
	Name        string            `yaml:"name,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Tags        []string          `yaml:"tags,omitempty"`
	Root        string            `yaml:"root,omitempty"`
	Matrix      []benchMatrixYAML `yaml:"matrix,omitempty"`
	ELO         benchELOYAML      `yaml:"elo"`
}

type benchMatrixYAML struct {
	ID     string                          `yaml:"id"`
	Agents map[string]benchMatrixAgentYAML `yaml:"agents,omitempty"`
}

type benchMatrixAgentYAML struct {
	Model  string `yaml:"model,omitempty"`
	System string `yaml:"system,omitempty"`
}

type benchELOYAML struct {
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoningEffort,omitempty"`
	Verbosity       string `yaml:"verbosity,omitempty"`
	Criteria        string `yaml:"criteria"`
}

func parseBenchYAML(data []byte) (Meta, []MatrixEntry, string, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Meta{}, nil, "", "", errors.New("missing bench.yaml")
	}

	var cfg benchYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Meta{}, nil, "", "", fmt.Errorf("bench.yaml: %w", err)
	}

	criteria := cfg.ELO.Criteria
	if strings.TrimSpace(criteria) == "" {
		return Meta{}, nil, "", "", errors.New("bench.yaml: elo.criteria is required")
	}

	judge, err := composeJudgeSelector(cfg.ELO)
	if err != nil {
		return Meta{}, nil, "", "", err
	}

	matrix, err := parseMatrixYAML(cfg.Matrix)
	if err != nil {
		return Meta{}, nil, "", "", err
	}

	meta := Meta{
		Name:        strings.TrimSpace(cfg.Name),
		Description: strings.TrimSpace(cfg.Description),
		Tags:        slices.Clone(cfg.Tags),
		Root:        strings.TrimSpace(cfg.Root),
	}

	return meta, matrix, criteria, judge, nil
}

func parseMatrixYAML(rows []benchMatrixYAML) ([]MatrixEntry, error) {
	if len(rows) == 0 {
		return nil, nil
	}

	seen := map[string]struct{}{}
	out := make([]MatrixEntry, 0, len(rows))
	for i, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			return nil, fmt.Errorf("bench.yaml: matrix[%d]: id is required", i)
		}

		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("bench.yaml: matrix id %q duplicated", id)
		}

		seen[id] = struct{}{}

		agents := map[string]MatrixAgent{}
		names := make([]string, 0, len(row.Agents))
		for name := range row.Agents {
			names = append(names, name)
		}

		slices.Sort(names)

		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("bench.yaml: matrix %q: empty agent name", id)
			}

			spec := row.Agents[name]
			override := MatrixAgent{System: spec.System}
			if m := strings.TrimSpace(spec.Model); m != "" {
				sel, err := parseModelSelector(m)
				if err != nil {
					return nil, fmt.Errorf("bench.yaml: matrix %q agents.%s.model: %w", id, name, err)
				}

				override.Model = sel
			}

			if override.Model.Raw == "" && strings.TrimSpace(override.System) == "" {
				return nil, fmt.Errorf("bench.yaml: matrix %q agents.%s: set model and/or system", id, name)
			}

			agents[name] = override
		}

		out = append(out, MatrixEntry{ID: id, Agents: agents})
	}

	return out, nil
}

type variationTurnsYAML struct {
	Turns []Message    `yaml:"turns"`
	Tools []ToolMock   `yaml:"tools,omitempty"`
	Bash  []BashDouble `yaml:"bash,omitempty"`
}

func parseTurnsYAML(data []byte) ([]Message, []ToolMock, []BashDouble, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, nil, errors.New("missing turns.yaml")
	}

	var cfg variationTurnsYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("turns.yaml: %w", err)
	}

	for i, double := range cfg.Bash {
		if strings.TrimSpace(double.Command) == "" && strings.TrimSpace(double.Pattern) == "" {
			return nil, nil, nil, fmt.Errorf("turns.yaml bash[%d]: command or pattern required", i)
		}
	}

	return cfg.Turns, cfg.Tools, cfg.Bash, nil
}

func formatTurnsYAML(turns []Message, tools []ToolMock, bash []BashDouble) ([]byte, error) {
	data, err := yaml.Marshal(&variationTurnsYAML{Turns: turns, Tools: tools, Bash: bash})
	if err != nil {
		return nil, err
	}

	return ensureTrailingNewline(data), nil
}

func composeJudgeSelector(elo benchELOYAML) (string, error) {
	model := strings.TrimSpace(elo.Model)
	if model == "" {
		return "", errors.New("bench.yaml: elo.model is required")
	}

	raw := model
	query := url.Values{}
	if v := strings.TrimSpace(elo.ReasoningEffort); v != "" {
		query.Set("reasoningEffort", v)
	}

	if v := strings.TrimSpace(elo.Verbosity); v != "" {
		query.Set("verbosity", v)
	}

	if encoded := query.Encode(); encoded != "" {
		raw = model + "?" + encoded
	}

	if _, err := parseModelSelector(raw); err != nil {
		return "", fmt.Errorf("bench.yaml: elo model: %w", err)
	}

	return raw, nil
}

func formatBenchYAML(meta Meta, matrix []MatrixEntry, criteria, judge string) ([]byte, error) {
	elo := benchELOYAML{Criteria: criteria}
	sel, err := parseModelSelector(strings.TrimSpace(judge))
	if err != nil {
		elo.Model = strings.TrimSpace(judge)
	} else {
		elo.Model = sel.Model
		elo.ReasoningEffort = sel.ReasoningEffort
		elo.Verbosity = sel.Verbosity
	}

	root := strings.TrimSpace(meta.Root)
	if root == "" {
		root = "main"
	}

	var matrixYAML []benchMatrixYAML
	for _, entry := range matrix {
		row := benchMatrixYAML{ID: entry.ID}
		if len(entry.Agents) > 0 {
			row.Agents = map[string]benchMatrixAgentYAML{}
			names := make([]string, 0, len(entry.Agents))
			for name := range entry.Agents {
				names = append(names, name)
			}

			slices.Sort(names)

			for _, name := range names {
				ov := entry.Agents[name]
				row.Agents[name] = benchMatrixAgentYAML{
					Model:  ov.Model.Raw,
					System: ov.System,
				}
			}
		}

		matrixYAML = append(matrixYAML, row)
	}

	cfg := benchYAML{
		Name:        meta.Name,
		Description: meta.Description,
		Tags:        meta.Tags,
		Root:        root,
		Matrix:      matrixYAML,
		ELO:         elo,
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return nil, err
	}

	return ensureTrailingNewline(data), nil
}

func membersFromBAR(bar *BAR) (map[string][]byte, error) {
	if len(bar.Agents) == 0 {
		return nil, errors.New("at least one agent is required")
	}

	benchData, err := formatBenchYAML(bar.Meta, bar.Matrix, bar.Criteria, bar.Judge)
	if err != nil {
		return nil, err
	}

	members := map[string][]byte{
		benchMember: benchData,
	}
	for name, data := range bar.Agents {
		members[agentsRoot+"/"+name+".md"] = ensureTrailingNewline(data)
	}

	for _, v := range bar.Variations {
		base := variationsRoot + "/" + v.ID + "/"
		if strings.TrimSpace(v.System) != "" {
			members[base+systemName] = ensureTrailingNewline([]byte(v.System))
		}

		turnsData, err := formatTurnsYAML(v.Transcript, v.Tools, v.BashDoubles)
		if err != nil {
			return nil, err
		}

		members[base+turnsName] = turnsData
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
	names := orderedMemberNames(members)
	files := make([]txtar.File, 0, len(names))
	for _, name := range names {
		files = append(files, txtar.File{Name: name, Data: members[name]})
	}

	return files
}

// orderedMemberNames puts principal-edit files first: bench.yaml, mocks, variations, then agents.
func orderedMemberNames(members map[string][]byte) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}

	slices.SortFunc(names, func(a, b string) int {
		if ra, rb := memberRank(a), memberRank(b); ra != rb {
			return cmp.Compare(ra, rb)
		}

		return cmp.Compare(a, b)
	})

	return names
}

func memberRank(name string) int {
	switch {
	case name == benchMember:
		return 0
	case strings.HasPrefix(name, variationsRoot+"/"):
		return 1
	case strings.HasPrefix(name, agentsRoot+"/"):
		return 2
	default:
		return 3
	}
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
