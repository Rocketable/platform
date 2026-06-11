package rocketcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

	permissionAllow = PermissionAllow
	permissionDeny  = PermissionDeny
)

// PermissionRule matches a subject pattern to a permission action.
type PermissionRule struct {
	Pattern string
	Action  PermissionAction
}

// PermissionBucket groups permission rules under a named permission category.
type PermissionBucket struct {
	Name  string
	Rules []PermissionRule
}

// PermissionSet contains all permission buckets configured for an agent.
type PermissionSet struct {
	Buckets []PermissionBucket
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
		if _, err := parsePermissionAction(node.Value); err != nil {
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

			rules, err := parsePermissionRules(node.Content[i+1])
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

func parsePermissionRules(node *yaml.Node) ([]PermissionRule, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		action, err := parsePermissionAction(node.Value)
		if err != nil {
			return nil, err
		}

		return []PermissionRule{{Pattern: "*", Action: action}}, nil
	case yaml.MappingNode:
		rules := []PermissionRule{}

		for i := 0; i+1 < len(node.Content); i += 2 {
			action, err := parsePermissionAction(node.Content[i+1].Value)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", node.Content[i].Value, err)
			}

			rules = append(rules, PermissionRule{Pattern: expandPermissionPattern(node.Content[i].Value), Action: action})
		}

		return rules, nil
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
		return nil, errors.New("permission rule must be a scalar or mapping")
	default:
		return nil, errors.New("permission rule must be a scalar or mapping")
	}
}

func parsePermissionAction(value string) (PermissionAction, error) {
	switch PermissionAction(value) {
	case PermissionAllow:
		return PermissionAllow, nil
	case PermissionDeny:
		return PermissionDeny, nil
	case "ask":
		return "", fmt.Errorf("permission action %q is not supported", value)
	default:
		return "", fmt.Errorf("unknown permission action %q", value)
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

func expandPermissionPattern(pattern string) string {
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
	if _, err := parsePermissionAction(string(action)); err != nil {
		return err
	}

	bucketName, err := normalizePermissionName(permission)
	if err != nil {
		return err
	}

	rule := PermissionRule{Pattern: expandPermissionPattern(pattern), Action: action}

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

func (ps PermissionSet) evaluate(permission, subject string) permissionDecision {
	decision := ps.evaluateRules(permission, subject)
	if permission == "read" && !decision.Matched {
		editDecision := ps.evaluateRules("edit", subject)
		if editDecision.Action == permissionAllow {
			editDecision.Permission = "read"
			return editDecision
		}
	}

	return decision
}

func (ps PermissionSet) evaluateRules(permission, subject string) permissionDecision {
	decision := permissionDecision{Action: permissionDeny, Bucket: "", Rule: PermissionRule{Pattern: "", Action: ""}, Matched: false, Permission: permission, Subject: subject}

	for _, bucket := range ps.Buckets {
		if bucket.Name != permission {
			continue
		}

		for _, rule := range bucket.Rules {
			if !permissionWildcardMatch(subject, rule.Pattern) {
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
	for _, bucket := range ps.Buckets {
		permissionBucket := bucket.Name == permission

		editBucketForRead := permission == "read" && bucket.Name == "edit"
		if !permissionBucket && !editBucketForRead {
			continue
		}

		for _, rule := range bucket.Rules {
			if rule.Action == permissionAllow {
				return true
			}
		}
	}

	return false
}

func permissionWildcardMatch(input, pattern string) bool {
	input = strings.ReplaceAll(input, "\\", "/")
	pattern = strings.ReplaceAll(pattern, "\\", "/")

	escaped := wildcardMeta.ReplaceAllStringFunc(pattern, func(s string) string { return `\` + s })
	escaped = strings.ReplaceAll(escaped, "*", ".*")

	escaped = strings.ReplaceAll(escaped, "?", ".")
	if before, ok := strings.CutSuffix(escaped, " .*"); ok {
		escaped = before + "( .*)?"
	}

	matched, err := regexp.MatchString("(?s)^"+escaped+"$", input)

	return err == nil && matched
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

func bashPermissionSubjects(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	parser := syntax.NewParser()

	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return []string{command}
	}

	subjects := []string{}
	printer := syntax.NewPrinter()

	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		var buf bytes.Buffer
		if err := printer.Print(&buf, call); err != nil {
			return true
		}

		subject := strings.TrimSpace(buf.String())
		if subject != "" {
			subjects = append(subjects, subject)
		}

		return true
	})

	if len(subjects) == 0 {
		return []string{command}
	}

	return subjects
}

func rootedPathSubject(path string) string {
	if path == "" || path == "." {
		return "."
	}

	return filepath.ToSlash(filepath.Clean(path))
}

func isDeniedEnvPath(path string) bool {
	base := filepath.Base(filepath.Clean(filepath.FromSlash(path)))
	base = strings.ReplaceAll(base, "\\", "/")
	base = filepath.Base(base)

	if strings.HasSuffix(base, ".env.example") {
		return false
	}

	return strings.HasSuffix(base, ".env") || strings.Contains(base, ".env.")
}

func deniedEnvAccessMessage(path string) string {
	return "access denied: .env files are blocked: " + path
}
