// Package agentlint checks effective RocketClaw agent systems for unsafe permission and delegation combinations.
package agentlint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
	"gopkg.in/yaml.v3"
)

const (
	rc000 = "RC000"
	rc001 = "RC001"
	rc002 = "RC002"
	rc003 = "RC003"
	rc004 = "RC004"
	rc005 = "RC005"
	rc006 = "RC006"
)

// Finding is one lint result line.
type Finding struct {
	Code     string
	Severity string
	Path     string
	Message  string
	keys     []string
}

// Result is the complete lint result for one runtime tree.
type Result struct {
	Findings []Finding
}

type agentInfo struct {
	agent        rocketcode.Agent
	filePath     string
	suppressions map[string][]suppression
}

type capability struct {
	agent   string
	file    string
	bucket  string
	pattern string
}

type suppression struct {
	code string
}

// Lint checks the effective agent system rooted at runtimeRoot.
func Lint(runtimeRoot string) (Result, error) {
	agentsRoot := filepath.Join(runtimeRoot, "agents")

	agentLoad := rocketcode.LoadAgents(os.DirFS(agentsRoot))
	if len(agentLoad.Errors) > 0 {
		return Result{}, fmt.Errorf("load agents: %w", agentLoad.Errors[0])
	}

	skillsRoot := filepath.Join(runtimeRoot, "skills")
	if _, err := os.Stat(skillsRoot); err == nil {
		skillLoad := rocketcode.LoadSkills(os.DirFS(skillsRoot), skillsRoot)
		if len(skillLoad.Errors) > 0 {
			return Result{}, fmt.Errorf("load skills: %w", skillLoad.Errors[0])
		}
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("stat skills: %w", err)
	}

	infos, findings, err := loadAgentInfos(agentsRoot, agentLoad.Agents)
	if err != nil {
		return Result{}, err
	}

	findings = append(findings, lintCapabilities(infos)...)
	findings = append(findings, lintTaskCycles(infos)...)
	findings = append(findings, lintDelegationEscalation(infos)...)
	findings = filterSuppressed(findings, infos)
	slices.SortFunc(findings, func(a, b Finding) int {
		if n := strings.Compare(a.Code, b.Code); n != 0 {
			return n
		}

		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}

		return strings.Compare(a.Message, b.Message)
	})

	return Result{Findings: findings}, nil
}

func loadAgentInfos(agentsRoot string, agents rocketcode.Agents) (map[string]*agentInfo, []Finding, error) {
	infos := map[string]*agentInfo{}
	findings := []Finding{}

	for name := range agents.Items {
		agent := agents.Items[name]
		filePath := filepath.ToSlash(filepath.Join("agents", agent.Location))

		data, err := os.ReadFile(filepath.Join(agentsRoot, agent.Location))
		if err != nil {
			return nil, nil, fmt.Errorf("read agent %s: %w", agent.Location, err)
		}

		frontmatter, err := parseFrontmatter(string(data))
		if err != nil {
			return nil, nil, fmt.Errorf("parse agent metadata %s: %w", agent.Location, err)
		}

		suppressions := map[string][]suppression{}
		findings = append(findings, collectMetadata(filePath, frontmatter, suppressions)...)
		infos[name] = &agentInfo{agent: agent, filePath: filePath, suppressions: suppressions}
	}

	return infos, findings, nil
}

func parseFrontmatter(content string) (*yaml.Node, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, errors.New("missing YAML frontmatter")
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, errors.New("missing YAML frontmatter terminator")
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content[4:4+end]), &root); err != nil {
		return nil, fmt.Errorf("unmarshal YAML frontmatter: %w", err)
	}

	if len(root.Content) == 0 {
		return nil, nil
	}

	return root.Content[0], nil
}

func collectMetadata(filePath string, frontmatter *yaml.Node, suppressions map[string][]suppression) []Finding {
	if frontmatter == nil || frontmatter.Kind != yaml.MappingNode {
		return nil
	}

	findings := []Finding{}

	for i := 0; i+1 < len(frontmatter.Content); i += 2 {
		key := frontmatter.Content[i]
		value := frontmatter.Content[i+1]

		if key.Value == "permissions" {
			findings = append(findings, Finding{Code: rc006, Severity: "error", Path: filePath, Message: "plural permissions frontmatter is ignored; use permission", keys: []string{"permissions"}})
		}

		collectSuppressions(filePath, key.Value, value, suppressions, &findings)
	}

	return findings
}

func collectSuppressions(filePath, key string, node *yaml.Node, suppressions map[string][]suppression, findings *[]Finding) {
	addSuppressionsFromNode(filePath, key, node, suppressions, findings)

	if node.Kind != yaml.MappingNode {
		return
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		childKey := node.Content[i].Value
		addSuppressionsFromNode(filePath, childKey, node.Content[i], suppressions, findings)
		collectSuppressions(filePath, childKey, node.Content[i+1], suppressions, findings)
	}
}

func addSuppressionsFromNode(filePath, key string, node *yaml.Node, suppressions map[string][]suppression, findings *[]Finding) {
	comments := []string{node.LineComment, node.HeadComment, node.FootComment}
	for _, comment := range comments {
		if !strings.Contains(comment, "#nolint") {
			continue
		}

		code, ok := parseNoLint(comment)
		if !ok {
			*findings = append(*findings, Finding{Code: rc000, Severity: "error", Path: filePath, Message: "malformed #nolint comment requires optional code and non-empty reason"})
			continue
		}

		if code != "" {
			if !validCode(code) {
				*findings = append(*findings, Finding{Code: rc000, Severity: "error", Path: filePath, Message: "unknown #nolint code " + code})
				continue
			}
		}

		suppressions[key] = append(suppressions[key], suppression{code: code})
	}
}

func validCode(code string) bool {
	return slices.Contains([]string{rc001, rc002, rc003, rc004, rc005, rc006}, code)
}

func parseNoLint(comment string) (string, bool) {
	_, after, _ := strings.Cut(comment, "#nolint")

	rest := strings.TrimSpace(after)
	if strings.HasPrefix(rest, ":") {
		return "", strings.TrimSpace(rest[1:]) != ""
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "RC") {
		return "", false
	}

	code := strings.TrimSuffix(fields[0], ":")
	afterCode := strings.TrimSpace(strings.TrimPrefix(rest[len(code):], ":"))

	return code, afterCode != ""
}

func lintCapabilities(infos map[string]*agentInfo) []Finding {
	findings := []Finding{}
	writes := capabilities(infos, "edit")
	reads := append(capabilities(infos, "read"), writes...)
	executes := capabilities(infos, "bash")

	for _, write := range writes {
		for _, execute := range executes {
			if write.agent == execute.agent && pathsOverlap(write.pattern, commandPath(execute.pattern)) {
				findings = append(findings, Finding{Code: rc001, Severity: "error", Path: write.file, Message: fmt.Sprintf("%s can edit %s and execute %s", write.agent, cleanSubject(write.pattern), cleanSubject(execute.pattern)), keys: []string{write.bucket, write.pattern, execute.bucket, execute.pattern}})
			}
		}
	}

	for _, read := range reads {
		for _, execute := range executes {
			if read.agent == execute.agent && pathsOverlap(read.pattern, commandPath(execute.pattern)) && read.pattern != execute.pattern {
				findings = append(findings, Finding{Code: rc002, Severity: "error", Path: read.file, Message: fmt.Sprintf("%s can read %s and execute constrained command %s", read.agent, cleanSubject(read.pattern), cleanSubject(execute.pattern)), keys: []string{read.bucket, read.pattern, execute.bucket, execute.pattern}})
			}
		}
	}

	externalWriters := []capability{}

	for name, info := range infos {
		if hasAllow(info.agent.Permission, "websearch") || hasAllow(info.agent.Permission, "webfetch") {
			for _, write := range writes {
				if write.agent == name {
					externalWriters = append(externalWriters, write)
				}
			}
		}
	}

	for _, write := range externalWriters {
		for _, read := range reads {
			if write.agent != read.agent && pathsOverlap(write.pattern, read.pattern) {
				findings = append(findings, Finding{Code: rc005, Severity: "error", Path: write.file + " -> " + read.file, Message: fmt.Sprintf("%s can write external content to %s that %s can read", write.agent, cleanSubject(write.pattern), read.agent), keys: []string{write.bucket, write.pattern, read.bucket, read.pattern}})
			}
		}
	}

	return findings
}

func lintTaskCycles(infos map[string]*agentInfo) []Finding {
	edges := taskEdges(infos)
	findings := []Finding{}

	for name := range infos {
		if reaches(edges, name, name, map[string]bool{}) && infos[name].agent.MaxRecursion == nil {
			findings = append(findings, Finding{Code: rc003, Severity: "error", Path: infos[name].filePath, Message: name + " participates in a task delegation cycle without bounded maxRecursion", keys: []string{"maxRecursion", "task"}})
		}
	}

	return findings
}

func lintDelegationEscalation(infos map[string]*agentInfo) []Finding {
	edges := taskEdges(infos)
	writes := capabilities(infos, "edit")
	executes := capabilities(infos, "bash")
	findings := []Finding{}

	for _, write := range writes {
		for _, execute := range executes {
			if write.agent == execute.agent || !reaches(edges, write.agent, execute.agent, map[string]bool{}) || !pathsOverlap(write.pattern, commandPath(execute.pattern)) {
				continue
			}

			findings = append(findings, Finding{Code: rc004, Severity: "error", Path: write.file + " -> " + execute.file, Message: fmt.Sprintf("%s can edit %s and delegate to %s, which can execute %s", write.agent, cleanSubject(write.pattern), execute.agent, cleanSubject(execute.pattern)), keys: []string{write.bucket, write.pattern, "task", execute.bucket, execute.pattern}})
		}
	}

	return findings
}

func capabilities(infos map[string]*agentInfo, bucket string) []capability {
	var caps []capability

	for name, info := range infos {
		for _, permissionBucket := range info.agent.Permission.Buckets {
			if permissionBucket.Name != bucket {
				continue
			}

			for _, rule := range permissionBucket.Rules {
				if rule.Action == rocketcode.PermissionAllow {
					caps = append(caps, capability{agent: name, file: info.filePath, bucket: bucket, pattern: rule.Pattern})
				}
			}
		}
	}

	return caps
}

func hasAllow(permission rocketcode.PermissionSet, bucket string) bool {
	for _, permissionBucket := range permission.Buckets {
		if permissionBucket.Name != bucket {
			continue
		}

		for _, rule := range permissionBucket.Rules {
			if rule.Action == rocketcode.PermissionAllow {
				return true
			}
		}
	}

	return false
}

func taskEdges(infos map[string]*agentInfo) map[string][]string {
	edges := map[string][]string{}

	for from, info := range infos {
		for to := range infos {
			if action, _ := info.agent.Permission.Evaluate("task", to); action == rocketcode.PermissionAllow {
				edges[from] = append(edges[from], to)
			}
		}
	}

	return edges
}

func reaches(edges map[string][]string, from, to string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}

	seen[from] = true
	for _, next := range edges[from] {
		if next == to || reaches(edges, next, to, seen) {
			return true
		}
	}

	return false
}

func pathsOverlap(a, b string) bool {
	a = normalizeSubject(a)

	b = normalizeSubject(b)
	if a == "*" || b == "*" || a == "" || b == "" {
		return true
	}

	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a) || strings.Contains(a, strings.TrimSuffix(b, "/*")) || strings.Contains(b, strings.TrimSuffix(a, "/*"))
}

func commandPath(pattern string) string {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		return pattern
	}

	if fields[0] == "bash" || fields[0] == "sh" {
		if len(fields) > 1 {
			return fields[1]
		}

		return fields[0]
	}

	return fields[0]
}

func normalizeSubject(subject string) string {
	subject = cleanSubject(commandPath(subject))
	if subject == "." {
		return ""
	}

	return subject
}

func cleanSubject(subject string) string {
	subject = strings.TrimPrefix(filepath.ToSlash(subject), "./")
	return strings.TrimPrefix(subject, "/")
}

func filterSuppressed(findings []Finding, infos map[string]*agentInfo) []Finding {
	filtered := findings[:0]
	for i := range findings {
		if suppressed(&findings[i], infos) {
			continue
		}

		filtered = append(filtered, findings[i])
	}

	return filtered
}

func suppressed(finding *Finding, infos map[string]*agentInfo) bool {
	for _, info := range infos {
		if !strings.Contains(finding.Path, info.filePath) {
			continue
		}

		for _, key := range finding.keys {
			for _, suppression := range info.suppressions[key] {
				if suppression.code == "" || suppression.code == finding.Code {
					return true
				}
			}
		}

		for _, suppression := range info.suppressions["*"] {
			if suppression.code == "" || suppression.code == finding.Code {
				return true
			}
		}
	}

	return false
}
