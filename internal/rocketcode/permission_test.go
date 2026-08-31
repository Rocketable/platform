package rocketcode

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/require"
)

func TestPermissionWildcardMatchOpenCodeSemantics(t *testing.T) {
	for _, tt := range []struct {
		input, pattern string
		want           bool
	}{
		{"./cli", "./cli", true}, {"./cli foo", "./cli", false}, {"./client", "./cli", false},
		{"./cli", "./cli*", true}, {"./cli foo", "./cli*", true}, {"./client", "./cli*", true},
		{"./cli", "./cli *", true}, {"./cli foo", "./cli *", true}, {"./client", "./cli *", false},
		{"README.md", "readme.md", false},
		{`dir\file.txt`, "dir/file.txt", true},
	} {
		require.Equal(t, tt.want, permissionWildcardMatch(tt.input, tt.pattern))
	}
}

func TestParsePermissionNode(t *testing.T) {
	t.Run("normalizes scalar permission", func(t *testing.T) {
		require.Equal(t, PermissionSet{Buckets: nil}, parsePermissionYAML(t, `allow`))
	})

	t.Run("aliases bash to shell", func(t *testing.T) {
		require.Equal(t, PermissionSet{Buckets: []PermissionBucket{
			{Name: "shell", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		}}, parsePermissionYAML(t, `
bash:
  "*": allow
`))
	})

	t.Run("shell bucket wins over bash", func(t *testing.T) {
		require.Equal(t, PermissionSet{Buckets: []PermissionBucket{
			{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
			{Name: "shell", Rules: []PermissionRule{{Pattern: "echo *", Action: permissionAllow}}},
		}}, parsePermissionYAML(t, `
bash:
  "*": allow
read: allow
shell:
  "echo *": allow
`))
		require.Equal(t, PermissionSet{Buckets: []PermissionBucket{
			{Name: "shell", Rules: []PermissionRule{{Pattern: "echo *", Action: permissionAllow}}},
			{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		}}, parsePermissionYAML(t, `
shell:
  "echo *": allow
bash:
  "*": allow
read: allow
`))
	})

	t.Run("normalizes nested permissions and edit aliases", func(t *testing.T) {
		require.Equal(t, PermissionSet{Buckets: []PermissionBucket{
			{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
			{Name: "edit", Rules: []PermissionRule{{Pattern: "*", Action: permissionDeny}, {Pattern: "docs/*.md", Action: permissionAllow}}},
			{Name: "shell", Rules: []PermissionRule{{Pattern: "*", Action: permissionDeny}, {Pattern: "git status *", Action: permissionAllow}}},
			{Name: "webfetch", Rules: []PermissionRule{{Pattern: "https://docs.example/*", Action: permissionAllow}}},
			{Name: "websearch", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		}}, parsePermissionYAML(t, `
read: allow
apply_patch:
  "*": deny
  "docs/*.md": allow
shell:
  "*": deny
  "git status *": allow
webfetch:
  "https://docs.example/*": allow
websearch: allow
`))
	})

	for input, want := range map[string]string{
		"websearch: {openai.com: allow}": `permission "websearch" only supports coarse allow or deny`,
		"read: ask":                      `permission action "ask" is not supported`,
		"doom_loop: deny":                `permission "doom_loop" is not supported`,
		"external_directory: deny":       `permission "external_directory" is not supported`,
	} {
		_, err := parsePermissionNode(parseYAMLNode(t, input))
		require.ErrorContains(t, err, want)
	}
}

func TestParsePermissionAuto(t *testing.T) {
	require.Equal(t, PermissionSet{Buckets: []PermissionBucket{{Name: "shell", Rules: []PermissionRule{
		{Pattern: "deploy *", Action: permissionAuto},
		{Pattern: "release *", Action: permissionAuto, Reviewer: "release-guardian"},
	}}}}, parsePermissionYAML(t, `shell: {"deploy *": auto, "release *": auto(release-guardian)}`))

	for _, input := range []string{
		`shell: {"*": auto()}`,
		`shell: {"*": auto(foo}`,
		`shell: {"*": auto(foo))}`,
	} {
		_, err := parsePermissionNode(parseYAMLNode(t, input))
		require.ErrorContains(t, err, "malformed permission action")
	}
}

func TestPermissionSetHelpers(t *testing.T) {
	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "docs/*.md"))
	require.NoError(t, permissions.Deny("read", "docs/private/*"))
	require.Equal(t, PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "docs/*.md", Action: PermissionAllow}, {Pattern: "docs/private/*", Action: PermissionDeny}}}}}, permissions)

	permissions = PermissionSet{Buckets: nil}
	require.NoError(t, permissions.Deny("tools", "current_time"))
	require.NoError(t, permissions.Allow("tools", "current_time"))
	require.Equal(t, PermissionAllow, permissions.evaluate("tools", "current_time").Action)

	permissions = PermissionSet{Buckets: nil}
	require.NoError(t, permissions.Allow("apply_patch", "*.go"))
	require.Equal(t, "edit", permissions.Buckets[0].Name)
	permissions = PermissionSet{Buckets: nil}
	require.NoError(t, permissions.Allow("bash", "echo *"))
	require.Equal(t, "shell", permissions.Buckets[0].Name)
	require.EqualError(t, permissions.Allow("external_directory", "*"), `permission "external_directory" is not supported`)
	require.EqualError(t, permissions.Set("read", "*", PermissionAction("ask")), `permission action "ask" is not supported`)
}

func TestPermissionSetEvaluate(t *testing.T) {
	tests := []struct {
		name, yaml, permission, subject string
		action                          PermissionAction
		matched                         bool
	}{
		{name: "default deny", yaml: `tools: {current_time: allow}`, permission: "tools", subject: "restart", action: PermissionDeny, matched: false},
		{name: "explicit deny", yaml: `tools: {restart: deny}`, permission: "tools", subject: "restart", action: PermissionDeny, matched: true},
		{name: "wildcard match", yaml: `rocketclaw: {restart_*: allow}`, permission: "rocketclaw", subject: "restart_tool", action: PermissionAllow, matched: true},
		{name: "last match wins", yaml: `tools: {"*": allow, restart: deny}`, permission: "tools", subject: "restart", action: PermissionDeny, matched: true},
		{name: "read inherits edit allow", yaml: `edit: {docs/*.md: allow}`, permission: "read", subject: "docs/guide.md", action: PermissionAllow, matched: true},
		{name: "global bucket is ignored", yaml: `"*": {"*": allow}`, permission: "shell", subject: "git status", action: PermissionDeny, matched: false},
		{name: "scalar allow is ignored", yaml: `allow`, permission: "shell", subject: "git status", action: PermissionDeny, matched: false},
		{name: "bash alias allows shell", yaml: `bash: {"*": allow}`, permission: "shell", subject: "git status", action: PermissionAllow, matched: true},
		{name: "shell wins over bash", yaml: "bash: {\"*\": allow}\nshell: {\"echo *\": allow}", permission: "shell", subject: "git status", action: PermissionDeny, matched: false},
		{name: "explicit shell allow remains effective", yaml: `shell: {"*": allow}`, permission: "shell", subject: "git status", action: PermissionAllow, matched: true},
		{name: "explicit auto remains effective", yaml: `shell: {"*": allow, "deploy *": auto}`, permission: "shell", subject: "deploy prod", action: PermissionAuto, matched: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, matched := parsePermissionYAML(t, tt.yaml).Evaluate(tt.permission, tt.subject)
			require.Equal(t, tt.action, action)
			require.Equal(t, tt.matched, matched)
		})
	}
}

func TestSkillReadPermissionsUseFoldedPaths(t *testing.T) {
	permissions := parsePermissionYAML(t, `read:
  "skills/parent/private*": deny
  "skills/parent/review*": auto
  "skills/parent/café.txt": deny`)
	permissions.skillRead = map[string]skillReadAccess{
		"skills/parent":       {allowed: true, folded: true},
		"skills/parent/child": {allowed: false, folded: true},
	}

	for _, tt := range []struct {
		path   string
		action PermissionAction
	}{
		{path: "skills/parent/ASSET.TXT", action: permissionAllow},
		{path: "skills/parent/CHILD/asset.txt", action: permissionDeny},
		{path: "skills/parent/PRIVATE.TXT", action: permissionDeny},
		{path: "skills/parent/REVIEW.TXT", action: permissionAuto},
		{path: "skills/parent/cafe\u0301.txt", action: permissionDeny},
	} {
		action, _ := permissions.Evaluate("read", tt.path)
		require.Equal(t, tt.action, action, tt.path)
	}
}

func TestReadPermissionInheritsEditAllow(t *testing.T) {
	tests := []struct {
		name, yaml, subject, bucket string
		action                      PermissionAction
		matched                     bool
	}{
		{name: "inherits exact edit allow", yaml: `edit: {"HALLY_GOOGLE_WORKSPACE.md": allow}`, subject: "HALLY_GOOGLE_WORKSPACE.md", action: permissionAllow, matched: true, bucket: "edit"},
		{name: "inherits glob edit allow", yaml: `edit: {"memories/*.md": allow}`, subject: "memories/today.md", action: permissionAllow, matched: true, bucket: "edit"},
		{name: "unmatched path remains denied", yaml: `edit: {"HALLY_GOOGLE_WORKSPACE.md": allow}`, subject: "other.md", action: permissionDeny, matched: false, bucket: ""},
		{name: "explicit read deny overrides edit allow", yaml: `edit: {"HALLY_GOOGLE_WORKSPACE.md": allow}
read: {"HALLY_GOOGLE_WORKSPACE.md": deny}`, subject: "HALLY_GOOGLE_WORKSPACE.md", action: permissionDeny, matched: true, bucket: "read"},
		{name: "explicit read allow overrides edit deny", yaml: `edit: {"HALLY_GOOGLE_WORKSPACE.md": deny}
read: {"HALLY_GOOGLE_WORKSPACE.md": allow}`, subject: "HALLY_GOOGLE_WORKSPACE.md", action: permissionAllow, matched: true, bucket: "read"},
		{name: "final edit deny does not inherit", yaml: `edit: {"*": allow, "secret.md": deny}`, subject: "secret.md", action: permissionDeny, matched: false, bucket: ""},
		{name: "non matching read rule still allows edit inheritance", yaml: `read: {"private.md": deny}
edit: {"public.md": allow}`, subject: "public.md", action: permissionAllow, matched: true, bucket: "edit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := parsePermissionYAML(t, tt.yaml).evaluate("read", tt.subject)
			require.Equal(t, tt.action, decision.Action)
			require.Equal(t, tt.matched, decision.Matched)
			require.Equal(t, tt.bucket, decision.Bucket)
			require.Equal(t, "read", decision.Permission)
		})
	}
}

func TestShellPermissionSubjects(t *testing.T) {
	require.Equal(t, []string{"git status", "git diff --stat"}, ShellPermissionSubjects("git status && git diff --stat"))
	require.Equal(t, []string{"echo $(date)", "date"}, ShellPermissionSubjects("echo $(date)"))
}

func parsePermissionYAML(t *testing.T, text string) PermissionSet {
	t.Helper()

	permissions, err := parsePermissionNode(parseYAMLNode(t, text))
	require.NoError(t, err)

	return permissions
}

func parseYAMLNode(t *testing.T, text string) *yaml.Node {
	t.Helper()

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &node))
	require.NotEmpty(t, node.Content)

	return node.Content[0]
}
