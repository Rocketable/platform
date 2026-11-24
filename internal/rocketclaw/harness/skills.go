package harness

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name                   string
	DisplayName            string
	Description            string
	License                string
	Compatibility          string
	DisableModelInvocation bool
	Location               string
	Content                string
}

type Skills struct {
	Root  string
	Items map[string]Skill

	dirs []string
	fsys fs.FS
}

type SkillLoadResult struct {
	Skills Skills
	Errors []error
}

type skillFrontmatter struct {
	Name                   string         `yaml:"name"`
	Description            string         `yaml:"description"`
	License                string         `yaml:"license"`
	Compatibility          string         `yaml:"compatibility"`
	Metadata               map[string]any `yaml:"metadata"`
	DisableModelInvocation any            `yaml:"disable-model-invocation"`
}

type skillReadDir struct {
	subject string
	rooted  bool
	folded  bool
}

var skillFrontmatterEntry = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\s*:\s*(.*)$`)

func LoadSkills(fsys fs.FS, root string) SkillLoadResult {
	result := SkillLoadResult{
		Skills: Skills{Root: root, Items: map[string]Skill{}, fsys: fsys},
		Errors: nil,
	}

	paths, errs := discoverSkills(fsys)
	result.Errors = errs

	for _, filePath := range paths {
		id := strings.TrimSuffix(filePath, ".md")
		if dir := path.Dir(filePath); path.Base(filePath) == "SKILL.md" {
			result.Skills.dirs = append(result.Skills.dirs, dir)

			id = path.Base(dir)
			if dir == "." {
				id = filepath.Base(root)
			}
		}

		skill, err := loadSkill(fsys, filePath, id)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}

		if existing, ok := result.Skills.Items[skill.Name]; ok {
			result.Errors = append(result.Errors, fmt.Errorf("%s: duplicate skill id %q overrides %s", filePath, skill.Name, existing.Location))
		}

		result.Skills.Items[skill.Name] = skill
	}

	slices.Sort(result.Skills.dirs)

	return result
}

func skillIgnoredName(name string) bool {
	switch name {
	case "node_modules", ".git", ".venv", "venv", "__pycache__":
		return true
	}

	return false
}

func discoverSkills(fsys fs.FS) ([]string, []error) {
	type dir struct {
		path  string
		depth int
	}

	var (
		paths []string
		errs  []error
	)

	queue := []dir{{path: ".", depth: 0}}
	for i := 0; i < len(queue); i++ {
		current := queue[i]

		entries, err := fs.ReadDir(fsys, current.path)
		if err != nil {
			errs = append(errs, fmt.Errorf("walk %q: %w", current.path, err))
			continue
		}

		var files, dirs []string

		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") || skillIgnoredName(entry.Name()) {
				continue
			}

			child := path.Join(current.path, entry.Name())

			mode := entry.Type()
			if mode&fs.ModeSymlink != 0 {
				info, err := fs.Stat(fsys, child)
				if err != nil {
					continue
				}

				mode = info.Mode().Type()
			}

			switch {
			case mode.IsDir():
				dirs = append(dirs, child)
			case mode.IsRegular():
				files = append(files, entry.Name())
			}
		}

		if slices.Contains(files, "SKILL.md") {
			paths = append(paths, path.Join(current.path, "SKILL.md"))
			continue
		}

		if current.depth == 0 {
			for _, file := range files {
				if strings.HasSuffix(file, ".md") {
					paths = append(paths, file)
				}
			}
		}

		if current.depth < 6 {
			for _, child := range dirs {
				queue = append(queue, dir{path: child, depth: current.depth + 1})
			}
		}
	}

	slices.Sort(paths)

	return paths, errs
}

func (s Skills) FindAvailable(query string, agent *Agent) string {
	list := s.advertised(agent)
	if len(list) == 0 {
		return "No skills are currently available."
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return formatSkills("## Matching skills", list)
	}

	normalizedQuery := strings.ToLower(query)
	tokens := strings.FieldsFunc(normalizedQuery, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	type scoredSkill struct {
		skill Skill
		score int
	}

	matches := []scoredSkill{}

	for _, skill := range list {
		score := scoreSkill(normalizedQuery, tokens, &skill)
		if score <= 0 {
			continue
		}

		matches = append(matches, scoredSkill{skill: skill, score: score})
	}

	if len(matches) == 0 {
		return "No matching skills found."
	}

	slices.SortFunc(matches, func(a, b scoredSkill) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(len(a.skill.Name), len(b.skill.Name)), strings.Compare(a.skill.Name, b.skill.Name))
	})

	for i := range matches {
		list[i] = matches[i].skill
	}

	return formatSkills("## Matching skills", list[:len(matches)])
}

func (s Skills) Available(agent *Agent) []Skill {
	return s.listed(func(skill *Skill) bool { return skillAllowedForAgent(skill, agent) })
}

func (s Skills) advertised(agent *Agent) []Skill {
	return s.listed(func(skill *Skill) bool {
		return skill.Description != "" && !skill.DisableModelInvocation && agent.Permission.evaluate("skill", skill.Name).Action != permissionDeny
	})
}

func (s Skills) listed(keep func(*Skill) bool) []Skill {
	list := make([]Skill, 0, len(s.Items))
	for _, skill := range s.Items {
		if keep(&skill) {
			list = append(list, skill)
		}
	}

	slices.SortFunc(list, func(a, b Skill) int {
		if order := strings.Compare(skillSortKey(a.Name), skillSortKey(b.Name)); order != 0 {
			return order
		}

		return cmp.Or(
			strings.Compare(cases.Fold().String(norm.NFD.String(a.Name)), cases.Fold().String(norm.NFD.String(b.Name))),
			strings.Compare(b.Name, a.Name))
	})

	return list
}

func skillAllowedForAgent(skill *Skill, agent *Agent) bool {
	return agent.Permission.evaluate("skill", skill.Name).Action == permissionAllow
}

func (s Skills) withReadPermissions(root *os.Root, agents Agents) Agents {
	var dirs map[string]skillReadDir

findAllowedSkill:
	for name := range agents.Items {
		agent := agents.Items[name]
		for _, skill := range s.Items {
			if skillAllowedForAgent(&skill, &agent) {
				dirs = s.readableDirs(root)
				break findAllowedSkill
			}
		}
	}

	if len(dirs) == 0 {
		return agents
	}

	skills := make(map[string]Skill, len(s.Items))
	for _, skill := range s.Items {
		skills[path.Dir(skill.Location)] = skill
	}

	items := maps.Clone(agents.Items)
	for name := range items {
		agent := items[name]
		read := make(map[string]skillReadAccess, len(dirs))
		allowed := false

		for dir, readable := range dirs {
			skill, ok := skills[dir]
			access := skillReadAccess{allowed: ok && readable.rooted && skillAllowedForAgent(&skill, &agent), folded: readable.folded}
			read[readable.subject] = access
			allowed = allowed || access.allowed
		}

		if !allowed {
			continue
		}

		agent.Permission.skillRead = read
		items[name] = agent
	}

	return Agents{Items: items}
}

func (s Skills) readableDirs(root *os.Root) map[string]skillReadDir {
	dirs := make(map[string]skillReadDir, len(s.dirs))
	for _, dir := range s.dirs {
		skillRoot, err := normalizeRootName(root, filepath.Join(s.Root, filepath.FromSlash(dir)))
		if err != nil || !filepath.IsLocal(skillRoot) {
			continue
		}

		readable := skillReadDir{subject: rootedPathSubject(skillRoot)}
		dirs[dir] = readable

		info, err := root.Lstat(skillRoot)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		skillInfo := info

		safe := true

		for parent := filepath.Dir(skillRoot); parent != "."; parent = filepath.Dir(parent) {
			info, err = root.Lstat(parent)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				safe = false
				break
			}
		}

		if !safe {
			continue
		}

		sourceInfo, errSource := fs.Stat(s.fsys, dir)
		localInfo, errLocal := root.Lstat(skillRoot)
		readable.rooted = errSource == nil && errLocal == nil && sourceInfo.Sys() != nil && localInfo.Sys() != nil && os.SameFile(sourceInfo, localInfo)

		alias := []byte(skillRoot)
		for i, char := range alias {
			if char >= 'a' && char <= 'z' {
				alias[i] -= 'a' - 'A'
				break
			}
		}

		aliasInfo, errAlias := root.Stat(string(alias))
		readable.folded = errAlias == nil && os.SameFile(skillInfo, aliasInfo)
		dirs[dir] = readable
	}

	return dirs
}

func formatSkills(heading string, skills []Skill) string {
	lines := make([]string, 1, len(skills)+1)

	lines[0] = heading
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- **%s**: %s", skill.Name, skill.Description))
	}

	return strings.Join(lines, "\n")
}

func composeSystemPromptWithSkills(base string, skills Skills, agent *Agent) string {
	prompts := []string{strings.TrimSpace(base)}

	if list := skills.advertised(agent); len(list) > 0 {
		lines := append(make([]string, 0, len(list)+5),
			"Skills provide specialized instructions and workflows for specific tasks.",
			"Use the skill tool to load a skill when a task matches its description.",
			"The user may also invoke a skill directly. When that happens, its instructions appear in the conversation as a <skill_content> block, the same shape the skill tool returns. A skill that is already present this way does not need to be invoked again.",
			"<available_skills>",
		)
		for _, skill := range list {
			lines = append(lines, "  <skill>\n    <id>"+skill.Name+"</id>\n    <name>"+skill.DisplayName+"</name>\n    <description>"+skill.Description+"</description>\n  </skill>")
		}

		prompts = append(prompts, strings.Join(append(lines, "</available_skills>"), "\n"))
	}

	if prompt := PermissionPrompt(agent.Permission); prompt != "" {
		prompts = append(prompts, prompt)
	}

	if dir := agent.CustomShell.Dir; dir != "" {
		prompt := "## Working Directory\n\nYour working directory is `" + dir + "`. Shell commands start there and relative paths resolve from it."
		if agent.CustomShell.Emulated {
			prompt += " It is the only folder where changes persist: `/tmp` is cleared after every command and every other folder is read-only."
		}

		prompts = append(prompts, prompt)
	}

	return strings.TrimSpace(strings.Join(prompts, "\n\n"))
}

func PermissionPrompt(permissions PermissionSet) string {
	lines := []string{}
	inlineCode := strings.NewReplacer("\r", " ", "\n", " ", "`", "\\`")

	for _, bucket := range permissions.Buckets {
		if bucket.Name == "*" || bucket.Name == "rocketclaw_tags" {
			continue
		}

		if len(lines) > 0 {
			lines = append(lines, "")
		}

		name := bucket.Name
		if name != "" {
			name = strings.ToUpper(name[:1]) + name[1:]
		}

		lines = append(lines, "## "+name+" Permissions", "", "Rules are listed in order. The last matching rule wins.", "")

		for _, rule := range bucket.Rules {
			var action string

			switch rule.Action {
			case permissionAllow:
				action = "Allow"
			case permissionDeny:
				action = "Deny"
			case permissionAuto:
				action = "Require automatic approval for"
				if rule.Reviewer != "" {
					action = "Require automatic approval by `" + inlineCode.Replace(rule.Reviewer) + "` for"
				}
			}

			line := "- " + action + " `" + inlineCode.Replace(rule.Pattern) + "`"
			if len(rule.Except) > 0 {
				excepts := make([]string, len(rule.Except))
				for i, except := range rule.Except {
					excepts[i] = "`" + inlineCode.Replace(except) + "`"
				}

				line += " except " + strings.Join(excepts, ", ")
			}

			lines = append(lines, line+".")
		}
	}

	if permissions.Sudo {
		lines = append(lines, "", "## Sudo Mode", "", "Every tool is available except outside MCP tools, which follow the Mcp rules above. Any call no rule above covers requires automatic approval.")
	}

	if len(lines) == 0 {
		return ""
	}

	lines = append(lines,
		"",
		"## Permission Wildcard Rules",
		"",
		"- `*` matches any text.",
		"- `?` matches any single character.",
		"- Path separators are normalized to `/`.",
		"- A pattern ending in space-star, like `git status *`, also matches the command without extra arguments.",
	)

	return strings.Join(lines, "\n")
}

func loadSkill(fsys fs.FS, filePath, id string) (Skill, error) {
	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: read skill: %w", filePath, err)
	}

	var frontmatter skillFrontmatter

	content := data
	if match := AgentFrontmatterPattern.FindSubmatchIndex(data); match != nil {
		if err := yaml.Unmarshal(data[match[2]:match[3]], &frontmatter); err != nil {
			frontmatter = skillFrontmatter{}
			if yaml.Unmarshal(quoteSkillFrontmatterColons(data[match[2]:match[3]]), &frontmatter) != nil {
				return Skill{}, fmt.Errorf("%s: parse YAML frontmatter: %w", filePath, err)
			}
		}

		content = data[match[1]:]
	}

	disabled, _ := skillFlag(frontmatter.DisableModelInvocation)
	if autoinvoke, ok := skillFlag(frontmatter.Metadata["opencode/autoinvoke"]); ok {
		disabled = !autoinvoke
	}

	return Skill{
		Name:                   id,
		DisplayName:            cmp.Or(frontmatter.Name, id),
		Description:            frontmatter.Description,
		License:                frontmatter.License,
		Compatibility:          frontmatter.Compatibility,
		DisableModelInvocation: disabled,
		Location:               filePath,
		Content:                string(content),
	}, nil
}

func quoteSkillFrontmatterColons(frontmatter []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(frontmatter), "\r\n", "\n"), "\n")
	for i, line := range lines {
		entry := skillFrontmatterEntry.FindStringSubmatch(line)
		if entry == nil {
			continue
		}

		value := strings.TrimSpace(entry[2])
		if value != ">" && value != "|" && !strings.HasPrefix(value, `"`) && !strings.HasPrefix(value, "'") && strings.Contains(value, ":") {
			lines[i] = entry[1] + ": |-\n  " + value
		}
	}

	return []byte(strings.Join(lines, "\n"))
}

func skillSortKey(id string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}

		return r
	}, norm.NFD.String(id))

	var key strings.Builder

	for _, r := range cases.Fold().String(stripped) {
		switch {
		case unicode.IsLetter(r):
			key.WriteByte(2)
		case unicode.IsDigit(r):
			key.WriteByte(1)
		default:
			key.WriteByte(0)
		}

		key.WriteRune(r)
	}

	return key.String()
}

func skillFlag(value any) (enabled, set bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case int:
		return value == 1, value == 0 || value == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "on", "1":
			return true, true
		case "false", "no", "off", "0":
			return false, true
		}
	}

	return false, false
}

func scoreSkill(normalizedQuery string, tokens []string, skill *Skill) int {
	name := strings.ToLower(skill.Name)
	description := strings.ToLower(skill.Description)
	content := strings.ToLower(skill.Content)

	score := 0

	switch {
	case name == normalizedQuery:
		score += 1000
	case strings.HasPrefix(name, normalizedQuery):
		score += 800
	case strings.Contains(name, normalizedQuery):
		score += 600
	}

	if strings.Contains(description, normalizedQuery) {
		score += 300
	}

	if strings.Contains(content, normalizedQuery) {
		score += 150
	}

	for _, token := range tokens {
		switch {
		case token == name:
			score += 120
		case strings.HasPrefix(name, token):
			score += 80
		case strings.Contains(name, token):
			score += 60
		}

		if strings.Contains(description, token) {
			score += 25
		}

		if strings.Contains(content, token) {
			score += 10
		}
	}

	return score
}

func (s Skills) skillFiles(location string) ([]string, error) {
	files := []string{}

	fsys := s.fsys
	if fsys == nil {
		return nil, errors.New("skills filesystem is not available")
	}

	if path.Base(location) != "SKILL.md" {
		return files, nil
	}

	dir := path.Dir(location)

	err := fs.WalkDir(fsys, dir, func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if filePath != dir && skillIgnoredName(d.Name()) {
				return fs.SkipDir
			}

			return nil
		}

		if d.Type()&fs.ModeSymlink != 0 || filepath.Base(filePath) == "SKILL.md" {
			return nil
		}

		files = append(files, filepath.ToSlash(filepath.Join(s.Root, filePath)))

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list skill files for %q: %w", dir, err)
	}

	sort.Strings(files)

	if len(files) > 10 {
		files = files[:10]
	}

	for i, file := range files {
		files[i] = fmt.Sprintf("<file>%s</file>", file)
	}

	return files, nil
}
