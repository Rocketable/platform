package rocketcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"
)

// PermissionAction determines whether a matched operation is allowed or denied.
type PermissionAction string

const (
	// PermissionAllow allows a matching operation.
	PermissionAllow PermissionAction = "allow"
	// PermissionDeny denies a matching operation.
	PermissionDeny PermissionAction = "deny"
	// PermissionAuto requires automatic reviewer approval for a matching operation.
	PermissionAuto PermissionAction = "auto"

	permissionAllow = PermissionAllow
	permissionDeny  = PermissionDeny
	permissionAuto  = PermissionAuto
)

// PermissionRule matches a subject pattern to a permission action.
type PermissionRule struct {
	Pattern  string
	Action   PermissionAction
	Reviewer string
}

// PermissionBucket groups permission rules under a named permission category.
type PermissionBucket struct {
	Name  string
	Rules []PermissionRule
}

// PermissionSet contains all permission buckets configured for an agent.
type PermissionSet struct {
	Buckets   []PermissionBucket
	skillRead map[string]skillReadAccess
}

type skillReadAccess struct {
	allowed bool
	folded  bool
}

type permissionDecision struct {
	Action     PermissionAction
	Bucket     string
	Rule       PermissionRule
	Matched    bool
	Permission string
	Subject    string
}

var wildcardMeta = regexp.MustCompile(`[.+^${}()|[\]\\]`)

func parsePermissionNode(node *yaml.Node) (PermissionSet, error) {
	if node == nil || node.Kind == 0 {
		return PermissionSet{Buckets: nil}, nil
	}

	switch node.Kind {
	case yaml.ScalarNode:
		if _, _, err := parsePermissionAction(node.Value); err != nil {
			return PermissionSet{}, err
		}

		return PermissionSet{Buckets: nil}, nil
	case yaml.MappingNode:
		buckets := []PermissionBucket{}

		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value

			bucketName, err := normalizePermissionName(key)
			if err != nil {
				return PermissionSet{}, err
			}

			if bucketName == "websearch" && node.Content[i+1].Kind == yaml.MappingNode {
				return PermissionSet{}, fmt.Errorf("permission %q only supports coarse allow or deny", key)
			}

			rules, err := parsePermissionRules(bucketName, node.Content[i+1])
			if err != nil {
				return PermissionSet{}, fmt.Errorf("permission %q: %w", key, err)
			}

			buckets = append(buckets, PermissionBucket{Name: bucketName, Rules: rules})
		}

		return PermissionSet{Buckets: buckets}, nil
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
		return PermissionSet{}, errors.New("permission must be a scalar or mapping")
	default:
		return PermissionSet{}, errors.New("permission must be a scalar or mapping")
	}
}

func parsePermissionRules(permission string, node *yaml.Node) ([]PermissionRule, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		action, reviewer, err := parsePermissionAction(node.Value)
		if err != nil {
			return nil, err
		}

		return []PermissionRule{{Pattern: "*", Action: action, Reviewer: reviewer}}, nil
	case yaml.MappingNode:
		rules := []PermissionRule{}

		for i := 0; i+1 < len(node.Content); i += 2 {
			action, reviewer, err := parsePermissionAction(node.Content[i+1].Value)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", node.Content[i].Value, err)
			}

			rules = append(rules, PermissionRule{Pattern: expandPermissionPattern(permission, node.Content[i].Value), Action: action, Reviewer: reviewer})
		}

		return rules, nil
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
		return nil, errors.New("permission rule must be a scalar or mapping")
	default:
		return nil, errors.New("permission rule must be a scalar or mapping")
	}
}

func parsePermissionAction(value string) (PermissionAction, string, error) {
	switch PermissionAction(value) {
	case PermissionAllow:
		return PermissionAllow, "", nil
	case PermissionDeny:
		return PermissionDeny, "", nil
	case PermissionAuto:
		return PermissionAuto, "", nil
	case "ask":
		return "", "", fmt.Errorf("permission action %q is not supported", value)
	default:
		if reviewer, ok := strings.CutPrefix(value, "auto("); ok {
			reviewer, closed := strings.CutSuffix(reviewer, ")")
			if !closed || reviewer == "" || strings.ContainsAny(reviewer, "()") {
				return "", "", fmt.Errorf("malformed permission action %q", value)
			}

			return PermissionAuto, reviewer, nil
		}

		return "", "", fmt.Errorf("unknown permission action %q", value)
	}
}

func normalizePermissionName(name string) (string, error) {
	switch name {
	case "apply_patch", "write", "patch":
		return "edit", nil
	case "external_directory", "doom_loop":
		return "", fmt.Errorf("permission %q is not supported", name)
	default:
		return name, nil
	}
}

func expandPermissionPattern(permission, pattern string) string {
	if permission == "bash" {
		return pattern
	}

	home := func() string {
		if dir, err := osUserHomeDir(); err == nil {
			return dir
		}

		return ""
	}

	if pattern == "~" {
		return home()
	}

	if strings.HasPrefix(pattern, "~/") {
		return home() + pattern[1:]
	}

	if pattern == "$HOME" {
		return home()
	}

	if strings.HasPrefix(pattern, "$HOME/") {
		return home() + pattern[5:]
	}

	return pattern
}

func osUserHomeDir() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get user home dir: %w", err)
	}

	return dir, nil
}

// Allow appends an allow rule for permission and pattern.
func (ps *PermissionSet) Allow(permission, pattern string) error {
	return ps.Set(permission, pattern, PermissionAllow)
}

// Deny appends a deny rule for permission and pattern.
func (ps *PermissionSet) Deny(permission, pattern string) error {
	return ps.Set(permission, pattern, PermissionDeny)
}

// Set appends a permission rule for permission and pattern.
func (ps *PermissionSet) Set(permission, pattern string, action PermissionAction) error {
	if _, _, err := parsePermissionAction(string(action)); err != nil {
		return err
	}

	bucketName, err := normalizePermissionName(permission)
	if err != nil {
		return err
	}

	rule := PermissionRule{Pattern: expandPermissionPattern(bucketName, pattern), Action: action}

	for i := range ps.Buckets {
		if ps.Buckets[i].Name == bucketName {
			ps.Buckets[i].Rules = append(ps.Buckets[i].Rules, rule)
			return nil
		}
	}

	ps.Buckets = append(ps.Buckets, PermissionBucket{Name: bucketName, Rules: []PermissionRule{rule}})

	return nil
}

// Evaluate returns the effective permission action for permission and subject.
// The matched result reports whether a configured rule explicitly matched.
// When matched is false, action is PermissionDeny, the default action.
func (ps PermissionSet) Evaluate(permission, subject string) (action PermissionAction, matched bool) {
	decision := ps.evaluate(permission, subject)
	return decision.Action, decision.Matched
}

func (ps PermissionSet) evaluate(permission, subject string, scripts ...string) permissionDecision {
	skillAllowed := false
	skillFolded := false
	skillSubject := rootedPathSubject(subject)
	matchedLength := -1

	if permission == "read" {
		for dir, access := range ps.skillRead {
			subjectPath, dirPath := skillSubject, dir
			if access.folded {
				subjectPath, dirPath = foldPermissionPath(subjectPath), foldPermissionPath(dirPath)
			}

			if (subjectPath == dirPath || strings.HasPrefix(subjectPath, dirPath+"/")) && len(dirPath) > matchedLength {
				skillAllowed = access.allowed
				skillFolded = access.folded
				matchedLength = len(dirPath)
			}
		}
	}

	decision := ps.evaluateRules(permission, subject, skillFolded, scripts...)
	if permission == "read" && !decision.Matched {
		if skillAllowed {
			return permissionDecision{Action: permissionAllow, Bucket: "read", Rule: PermissionRule{Pattern: skillSubject, Action: permissionAllow}, Matched: true, Permission: "read", Subject: subject}
		}

		editDecision := ps.evaluateRules("edit", subject, skillFolded)
		if editDecision.Action == permissionAllow {
			editDecision.Permission = "read"
			return editDecision
		}
	}

	return decision
}

func (ps PermissionSet) evaluateRules(permission, subject string, folded bool, scripts ...string) permissionDecision {
	decision := permissionDecision{Action: permissionDeny, Bucket: "", Rule: PermissionRule{Pattern: "", Action: ""}, Matched: false, Permission: permission, Subject: subject}

	for _, bucket := range ps.Buckets {
		if bucket.Name != permission {
			continue
		}

		for _, rule := range bucket.Rules {
			input, pattern := subject, rule.Pattern
			if folded {
				input, pattern = foldPermissionPath(input), foldPermissionPath(pattern)
			}

			var matches bool
			if permission == "bash" && len(scripts) > 0 {
				matches = bashComponentPatternMatch(input, pattern) ||
					slices.ContainsFunc(scripts, func(script string) bool {
						return bashScriptPatternMatch(script, rule.Pattern)
					})
			} else {
				matches = permissionWildcardMatch(input, pattern)
			}

			if !matches {
				continue
			}

			decision.Action = rule.Action
			decision.Bucket = bucket.Name
			decision.Rule = rule
			decision.Matched = true
		}
	}

	return decision
}

func (ps PermissionSet) hasAllowRuleForPermission(permission string) bool {
	return ps.hasRuleForPermission(permission, permissionAllow)
}

func (ps PermissionSet) hasActionableRuleForPermission(permission string) bool {
	return ps.hasRuleForPermission(permission, permissionAllow, permissionAuto)
}

func (ps PermissionSet) hasRuleForPermission(permission string, actions ...PermissionAction) bool {
	if permission == "read" && slices.Contains(actions, permissionAllow) {
		for _, access := range ps.skillRead {
			if access.allowed {
				return true
			}
		}
	}

	for _, bucket := range ps.Buckets {
		permissionBucket := bucket.Name == permission

		editBucketForRead := permission == "read" && bucket.Name == "edit"
		if !permissionBucket && !editBucketForRead {
			continue
		}

		for _, rule := range bucket.Rules {
			if slices.Contains(actions, rule.Action) {
				return true
			}
		}
	}

	return false
}

func permissionWildcardMatch(input, pattern string) bool {
	escaped := wildcardMeta.ReplaceAllStringFunc(pattern, func(s string) string { return `\` + s })
	escaped = strings.ReplaceAll(escaped, "*", ".*")

	escaped = strings.ReplaceAll(escaped, "?", ".")
	if before, ok := strings.CutSuffix(escaped, " .*"); ok {
		escaped = before + "( .*)?"
	}

	matched, err := regexp.MatchString("(?s)^"+escaped+"$", input)

	return err == nil && matched
}

func foldPermissionPath(path string) string {
	return norm.NFC.String(strings.ToLower(path))
}

func canonicalToolArguments(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}

	buf, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}

	return string(buf)
}

// BashPermissionSubjects returns independently authorized shell operations.
// Unparseable syntax and unresolved executable names deny the entire script.
func BashPermissionSubjects(command string) []string {
	// The parser normalizes CR and NUL bytes; the executed argument must not differ.
	if strings.ContainsAny(command, "\r\x00") {
		return nil
	}

	parser := syntax.NewParser()

	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	return bashPermissionNodes(file, false)
}

func bashPermissionNodes(file *syntax.File, rule bool) []string {
	var subjects []string

	printer := syntax.NewPrinter()

	for node := range syntax.Preorder(file) {
		var (
			printed       []syntax.Node
			expandedWords []*syntax.Word
		)

		switch node := node.(type) {
		case *syntax.File, *syntax.Comment, *syntax.Lit, *syntax.Word,
			*syntax.Assign, *syntax.ArrayExpr, *syntax.ArrayElem, *syntax.CStyleLoop,
			*syntax.CaseItem, *syntax.BinaryArithm, *syntax.UnaryArithm, *syntax.ParenArithm,
			*syntax.BinaryTest, *syntax.UnaryTest, *syntax.ParenTest, *syntax.IfClause:
			// These are data or children of an independently checked operation.
		case *syntax.Stmt:
			if node.Coprocess || node.Disown {
				return nil
			}

			if node.Negated {
				subjects = append(subjects, "!")
			}

			if node.Background {
				subjects = append(subjects, "&")
			}

			// elif/else belong to the outer if; only that clause owns a statement.
			if clause, ok := node.Cmd.(*syntax.IfClause); ok {
				printed = append(printed, clause)
			}
		case *syntax.BinaryCmd:
			switch node.Op {
			case syntax.AndStmt, syntax.OrStmt, syntax.Pipe:
			case syntax.PipeAll:
				subjects = append(subjects, "|&")
			default:
				return nil
			}
		case *syntax.CallExpr:
			for _, assign := range node.Assigns {
				printed = append(printed, assign)
			}

			if len(node.Args) > 0 {
				// Argument expansion is an explicit grant; an unknown executable is not.
				if !rule && !bashStaticExecutable(node.Args[0]) {
					return nil
				}

				printed = append(printed, &syntax.CallExpr{Args: node.Args})
			}

			expandedWords = node.Args
		case *syntax.WordIter:
			expandedWords = node.Items
		case *syntax.Redirect:
			printed = append(printed, &syntax.Stmt{Position: node.Pos(), Redirs: []*syntax.Redirect{node}})
		case *syntax.SglQuoted:
			if node.Dollar {
				printed = append(printed, node)
			}
		case *syntax.DblQuoted:
			if node.Dollar {
				printed = append(printed, node)
			}
		case *syntax.DeclClause, *syntax.Subshell, *syntax.Block,
			*syntax.WhileClause, *syntax.ForClause, *syntax.FuncDecl, *syntax.CaseClause,
			*syntax.TestClause, *syntax.ArithmCmd, *syntax.LetClause, *syntax.TimeClause,
			*syntax.CoprocClause, *syntax.ParamExp, *syntax.ArithmExp,
			*syntax.CmdSubst, *syntax.ProcSubst, *syntax.ExtGlob:
			printed = append(printed, node)
		default:
			return nil
		}

		for _, word := range expandedWords {
			// Permission wildcards match argument text; they are not shell expansions.
			if !rule && bashWordExpansion(word) {
				printed = append(printed, word)
			}
		}

		for _, node := range printed {
			var buf bytes.Buffer
			if err := printer.Print(&buf, node); err != nil {
				return nil
			}

			subjects = append(subjects, buf.String())
		}
	}

	return subjects
}

func bashComponentPatternMatch(subject, pattern string) bool {
	if !permissionWildcardMatch(subject, pattern) {
		return false
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(pattern), "")
	if err != nil {
		// Component patterns such as "for *" need not be complete scripts.
		return true
	}

	if len(file.Stmts) != 1 {
		return false
	}

	stmt := file.Stmts[0]
	if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Assigns) > 0 && (len(call.Args) > 0 || len(call.Assigns) > 1) {
		return false
	}

	_, chain := stmt.Cmd.(*syntax.BinaryCmd)

	return !chain && (len(stmt.Redirs) == 0 || stmt.Cmd == nil && bashScriptPatternMatch(subject, pattern))
}

func bashScriptPatternMatch(command, pattern string) bool {
	if !permissionWildcardMatch(command, pattern) || strings.ContainsAny(command, "\r\x00") || strings.ContainsAny(pattern, "\r\x00") {
		return false
	}

	if command == pattern && !strings.ContainsAny(pattern, "*?") {
		return true
	}

	var (
		shapes   [2]string
		subjects [2][]string
	)

	for i, text := range []string{command, pattern} {
		file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
		if err != nil {
			return false
		}

		subjects[i] = bashPermissionNodes(file, i == 1)

		var (
			shape    strings.Builder
			retained []bool
		)

		syntax.Walk(file, func(node syntax.Node) bool {
			if node == nil {
				if retained[len(retained)-1] {
					shape.WriteByte(')')
				}

				retained = retained[:len(retained)-1]

				return true
			}

			keep := true

			switch node := node.(type) {
			case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, *syntax.Comment:
				keep = false
			case *syntax.BinaryCmd:
				shape.WriteString(node.Op.String())
			case *syntax.Redirect:
				shape.WriteString(node.Op.String())
			}

			if keep {
				fmt.Fprintf(&shape, "(%T", node)
			}

			retained = append(retained, keep)

			return true
		})

		shapes[i] = shape.String()
	}
	// Matching the raw text alone lets '*' consume separators or substitutions.
	// Require the same operation tree and match each operation independently.
	if shapes[0] != shapes[1] || len(subjects[0]) == 0 {
		return false
	}

	return slices.EqualFunc(subjects[0], subjects[1], permissionWildcardMatch)
}

func bashStaticExecutable(word *syntax.Word) bool {
	for part := range syntax.Preorder(word) {
		switch part := part.(type) {
		case *syntax.Word, *syntax.Lit:
		case *syntax.SglQuoted:
			if part.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if part.Dollar {
				return false
			}
		default:
			return false
		}
	}

	return !bashWordExpansion(word)
}

func bashWordExpansion(word *syntax.Word) bool {
	copyWord := *word
	syntax.SplitBraces(&copyWord)

	for _, part := range copyWord.Parts {
		if _, ok := part.(*syntax.BraceExp); ok {
			return true
		}
	}

	assignment := false
	bracket := 0 // 0: outside, 1: opening, 2: negated opening, 3: has a member.

	for partIndex, part := range word.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			if bracket != 0 && part.End().Offset()-part.Pos().Offset() > 2 {
				bracket = 3
			}

			continue
		}

		valueStart := -1

		if partIndex == 0 {
			name, _, found := strings.Cut(lit.Value, "=")

			assignment = found && syntax.ValidName(strings.TrimSuffix(strings.ReplaceAll(name, "\\\n", ""), "+"))
			valueStart = len(name) + 1
		}

		tildeStart := partIndex == 0

		for i := 0; i < len(lit.Value); i++ {
			if lit.Value[i] == '~' && tildeStart {
				return true
			}

			switch lit.Value[i] {
			case '\\':
				i++
				tildeStart = false

				if bracket != 0 {
					bracket = 3
				}

				continue
			case '*', '?':
				return true
			case '[':
				if bracket == 0 {
					bracket = 1
				} else {
					bracket = 3
				}
			case ']':
				if bracket == 3 {
					return true
				}

				if bracket != 0 {
					bracket = 3
				}
			case '!', '^':
				if bracket == 1 {
					bracket = 2
				} else if bracket != 0 {
					bracket = 3
				}
			default:
				if bracket != 0 {
					bracket = 3
				}
			}

			tildeStart = assignment && (i+1 == valueStart || lit.Value[i] == ':')
		}
	}

	return false
}

func rootedPathSubject(path string) string {
	if path == "" || path == "." {
		return "."
	}

	return filepath.ToSlash(filepath.Clean(path))
}

func isDeniedEnvPath(path string) bool {
	base := strings.ToLower(filepath.Base(filepath.Clean(path)))

	if strings.HasSuffix(base, ".env.example") {
		return false
	}

	return strings.HasSuffix(base, ".env") || strings.Contains(base, ".env.")
}

func deniedEnvAccessMessage(path string) string {
	return "access denied: .env files are blocked: " + path
}
