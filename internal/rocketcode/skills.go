package rocketcode

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

var (
	skillFrontmatterPattern = regexp.MustCompile(`\A---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|\z)`)
	skillNamePattern        = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// Skill contains a single preloaded skill definition.
type Skill struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]any
	Location      string
	Content       string
}

// Skills contains all discovered skills keyed by name.
type Skills struct {
	Root  string
	Items map[string]Skill
	Dirs  []string

	fsys fs.FS
}

// SkillLoadResult contains loaded skills and any non-fatal load errors.
type SkillLoadResult struct {
	Skills Skills
	Errors []error
}

type skillFrontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	Metadata      map[string]any `yaml:"metadata"`
}

// LoadSkills scans fsys for SKILL.md files and preloads valid skills.
func LoadSkills(fsys fs.FS, root string) SkillLoadResult {
	result := SkillLoadResult{
		Skills: Skills{Root: root, Items: map[string]Skill{}, Dirs: nil, fsys: fsys},
		Errors: nil,
	}

	var paths []string

	dirs := map[string]struct{}{}

	err := fs.WalkDir(fsys, ".", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("walk %q: %w", filePath, err))
			return nil
		}

		if d.IsDir() || path.Base(filePath) != "SKILL.md" {
			return nil
		}

		paths = append(paths, filePath)
		dirs[path.Dir(filePath)] = struct{}{}

		return nil
	})
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("walk skills: %w", err))
	}

	sort.Strings(paths)

	for _, filePath := range paths {
		skill, err := loadSkill(fsys, filePath)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}

		if existing, ok := result.Skills.Items[skill.Name]; ok {
			result.Errors = append(result.Errors, fmt.Errorf("%s: duplicate skill name %q overrides %s", filePath, skill.Name, existing.Location))
		}

		result.Skills.Items[skill.Name] = skill
	}

	result.Skills.Dirs = make([]string, 0, len(dirs))
	for dir := range dirs {
		result.Skills.Dirs = append(result.Skills.Dirs, dir)
	}

	sort.Strings(result.Skills.Dirs)

	return result
}

// Find returns all matching skills ranked by relevance.
func (s Skills) Find(query string) string {
	if len(s.Items) == 0 {
		return "No skills are currently available."
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return renderSkillMatches(s.sortedSkills())
	}

	normalizedQuery := normalizeSkillSearchText(query)
	tokens := tokenizeSkillQuery(query)

	type scoredSkill struct {
		skill Skill
		score int
	}

	matches := []scoredSkill{}

	for _, skill := range s.Items {
		score := scoreSkill(normalizedQuery, tokens, &skill)
		if score <= 0 {
			continue
		}

		matches = append(matches, scoredSkill{skill: skill, score: score})
	}

	if len(matches) == 0 {
		return "No matching skills found."
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}

		if len(matches[i].skill.Name) != len(matches[j].skill.Name) {
			return len(matches[i].skill.Name) < len(matches[j].skill.Name)
		}

		return matches[i].skill.Name < matches[j].skill.Name
	})

	ordered := make([]Skill, 0, len(matches))
	for _, match := range matches {
		ordered = append(ordered, match.skill)
	}

	return renderSkillMatches(ordered)
}

func availableSkills(items map[string]Skill, agent *Agent) []Skill {
	list := make([]Skill, 0, len(items))
	for _, skill := range items {
		if !skillAllowedForAgent(&skill, agent) {
			continue
		}

		list = append(list, skill)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list
}

func skillAllowedForAgent(skill *Skill, agent *Agent) bool {
	if agent == nil {
		return false
	}

	decision := agent.Permission.evaluate("skill", skill.Name)

	return decision.Action == permissionAllow
}

// FindAvailable returns visible skills matching query as user-facing text.
func (s Skills) FindAvailable(query string, agent *Agent) string {
	items := map[string]Skill{}
	for _, skill := range availableSkills(s.Items, agent) {
		items[skill.Name] = skill
	}

	return (Skills{Root: s.Root, Items: items, Dirs: s.Dirs, fsys: s.fsys}).Find(query)
}

func formatAvailableSkills(skills []Skill) string {
	if len(skills) == 0 {
		return "No skills are currently available."
	}

	lines := []string{"## Available skills"}
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- **%s**: %s", skill.Name, skill.Description))
	}

	return strings.Join(lines, "\n")
}

func composeSystemPromptWithSkills(base string, skills Skills, agent *Agent) string {
	prompts := []string{strings.TrimSpace(base)}

	if len(availableSkills(skills.Items, agent)) > 0 {
		prompts = append(prompts, "skills provide specialized instructions and workflows for specific tasks."+"\n"+"When a task may benefit from specialized instructions, call the find_skills tool to search all available skills, then call the skill tool to load the selected skill.")
	}

	if agent != nil {
		if prompt := permissionPrompt(agent.Permission); prompt != "" {
			prompts = append(prompts, prompt)
		}
	}

	return strings.TrimSpace(strings.Join(prompts, "\n\n"))
}

func permissionPrompt(permissions PermissionSet) string {
	lines := []string{}

	for _, bucket := range permissions.Buckets {
		if bucket.Name == "*" || !permissionRulesHaveAllow(bucket.Rules) {
			continue
		}

		if len(lines) > 0 {
			lines = append(lines, "")
		}

		lines = append(lines, "## Allowed "+permissionPromptName(bucket.Name)+" Permissions", "")
		lines = append(lines, permissionRuleLines(bucket.Name, bucket.Rules)...)
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

func permissionRulesHaveAllow(rules []PermissionRule) bool {
	for _, rule := range rules {
		if rule.Action == permissionAllow {
			return true
		}
	}

	return false
}

func permissionRuleLines(bucketName string, rules []PermissionRule) []string {
	fullAllowIndex := -1

	for i, rule := range rules {
		if rule.Pattern == "*" && rule.Action == permissionAllow {
			fullAllowIndex = i
		}
	}

	if fullAllowIndex >= 0 {
		exceptions := []string{}

		for _, rule := range rules[fullAllowIndex+1:] {
			if rule.Action == permissionDeny {
				exceptions = append(exceptions, rule.Pattern)
			}
		}

		if len(exceptions) == 0 {
			return []string{"- Everything is allowed."}
		}

		lines := []string{"- Everything is allowed except:"}
		if bucketName == "bash" {
			lines[0] = "- All commands are allowed except:"
		}

		for _, pattern := range exceptions {
			lines = append(lines, "- `"+markdownInlineCode(pattern)+"`")
		}

		return lines
	}

	lines := []string{}

	for _, rule := range rules {
		if rule.Action == permissionAllow {
			lines = append(lines, "- `"+markdownInlineCode(rule.Pattern)+"`")
		}
	}

	return lines
}

func permissionPromptName(name string) string {
	if name == "" {
		return name
	}

	return strings.ToUpper(name[:1]) + name[1:]
}

func markdownInlineCode(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")

	return strings.ReplaceAll(text, "`", "\\`")
}

func loadSkill(fsys fs.FS, filePath string) (Skill, error) {
	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: read skill: %w", filePath, err)
	}

	content := string(data)

	match := skillFrontmatterPattern.FindStringSubmatchIndex(content)
	if match == nil {
		return Skill{}, fmt.Errorf("%s: missing YAML frontmatter", filePath)
	}

	var frontmatter skillFrontmatter
	if err := yaml.Unmarshal([]byte(content[match[2]:match[3]]), &frontmatter); err != nil {
		return Skill{}, fmt.Errorf("%s: parse YAML frontmatter: %w", filePath, err)
	}

	if err := validateSkillFrontmatter(filePath, frontmatter); err != nil {
		return Skill{}, err
	}

	return Skill{
		Name:          frontmatter.Name,
		Description:   frontmatter.Description,
		License:       frontmatter.License,
		Compatibility: frontmatter.Compatibility,
		Metadata:      frontmatter.Metadata,
		Location:      filePath,
		Content:       content[match[1]:],
	}, nil
}

func validateSkillFrontmatter(filePath string, frontmatter skillFrontmatter) error {
	if frontmatter.Name == "" {
		return fmt.Errorf("%s: missing required frontmatter field %q", filePath, "name")
	}

	if len(frontmatter.Name) > 64 {
		return fmt.Errorf("%s: skill name exceeds 64 characters", filePath)
	}

	if !skillNamePattern.MatchString(frontmatter.Name) {
		return fmt.Errorf("%s: invalid skill name %q", filePath, frontmatter.Name)
	}

	expectedDir := path.Base(path.Dir(filePath))
	if frontmatter.Name != expectedDir {
		return fmt.Errorf("%s: skill name %q does not match directory %q", filePath, frontmatter.Name, expectedDir)
	}

	if frontmatter.Description == "" {
		return fmt.Errorf("%s: missing required frontmatter field %q", filePath, "description")
	}

	if len(frontmatter.Description) > 1024 {
		return fmt.Errorf("%s: skill description exceeds 1024 characters", filePath)
	}

	if strings.Contains(frontmatter.Name, "--") {
		return fmt.Errorf("%s: invalid skill name %q", filePath, frontmatter.Name)
	}

	return nil
}

func (s Skills) sortedSkills() []Skill {
	list := make([]Skill, 0, len(s.Items))
	for _, skill := range s.Items {
		list = append(list, skill)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list
}

func renderSkillMatches(skills []Skill) string {
	lines := make([]string, 0, len(skills)+1)

	lines = append(lines, "## Matching skills")
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- **%s**: %s", skill.Name, skill.Description))
	}

	return strings.Join(lines, "\n")
}

func normalizeSkillSearchText(text string) string {
	return strings.ToLower(text)
}

func tokenizeSkillQuery(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}

		tokens = append(tokens, part)
	}

	return tokens
}

func scoreSkill(normalizedQuery string, tokens []string, skill *Skill) int {
	name := normalizeSkillSearchText(skill.Name)
	description := normalizeSkillSearchText(skill.Description)
	content := normalizeSkillSearchText(skill.Content)

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

func (s Skills) skillFiles(dir string) ([]string, error) {
	files := []string{}

	fsys := s.fsys
	if fsys == nil {
		return nil, errors.New("skills filesystem is not available")
	}

	err := fs.WalkDir(fsys, dir, func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() || filepath.Base(filePath) == "SKILL.md" {
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

	wrapped := make([]string, 0, len(files))
	for _, file := range files {
		wrapped = append(wrapped, fmt.Sprintf("<file>%s</file>", file))
	}

	return wrapped, nil
}

func fileURL(filePath string) string {
	return "file://" + filepath.ToSlash(filePath)
}
