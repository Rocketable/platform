package justbash

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func frontmatter(t *testing.T, doc string) map[string]any {
	t.Helper()

	var fm map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &fm))

	return fm
}

func TestShell(t *testing.T) {
	t.Parallel()

	_, ok, err := Shell(frontmatter(t, "model: x\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = Shell(frontmatter(t, "shell: /bin/sandbox\nworkingDirectory: data\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
	require.False(t, ok, "a program path is not the emulated shell")

	_, _, err = Shell(frontmatter(t, "shell: emulated\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.EqualError(t, err, "workingDirectory: required with shell: emulated")

	shell, ok, err := Shell(frontmatter(t, "shell: emulated\nmodel: x\nworkingDirectory: data\ncommands: {hello: printf hi}\nallowedNetworkTargets: [api.stripe.com]\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, shell.Emulated)
	require.Equal(t, "/ws/.rocketclaw/justbash/wrapper.ts", shell.Path)
	require.JSONEq(t, `{"workspace":"/ws","runtimeDir":"/ws/.rocketclaw","workingDirectory":"data","commands":{"hello":"printf hi"},"allowedNetworkTargets":["api.stripe.com"]}`, string(shell.Setup))
}

// TestJustBash installs just-bash with npm and runs commands through the wrapper as the harness does.
func TestJustBash(t *testing.T) {
	t.Parallel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	runtimeDir, tmp := filepath.Join(workspace, ".rocketclaw"), filepath.Join(workspace, ".rocketclaw", ".rocketcode", "tmp", "c1")
	dir := filepath.Join(runtimeDir, "justbash")

	require.NoError(t, os.MkdirAll(tmp, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "outside.txt"), []byte("outside"), 0o600))
	require.NoError(t, Install(dir))
	require.NoError(t, Install(dir), "an installed just-bash installs again without npm")

	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("listed")) }))
	t.Cleanup(listed.Close)

	unlisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("unlisted")) }))
	t.Cleanup(unlisted.Close)

	folder, wd := filepath.Join(workspace, "justbash", "c1"), "workingDirectory: justbash/$ROCKETCLAW_CONVERSATION_ID\n"
	listedTarget := wd + "allowedNetworkTargets: [" + strings.TrimPrefix(listed.URL, "http://") + "]\n"

	cases := []struct {
		name, settings, command, output string
		success                         bool
	}{
		{"writes land in the folder", wd, "echo hi > f.txt; cat f.txt; pwd", "hi\n" + folder + "\n", true},
		{"files outside the folder are invisible", wd, "cat " + filepath.Join(workspace, "outside.txt"), "No such file or directory", false},
		{"the host TMPDIR is invisible", wd, "ls " + tmp, "No such file or directory", false},
		{"TMPDIR stays in memory", wd, "f=$(mktemp) && echo t > $f && cat $f && echo $TMPDIR", "t\n/tmp\n", true},
		{"python", wd, `python3 -c "print(6*7)"`, "42\n", true},
		{"javascript", wd, `js-exec -c "console.log(6*7)"`, "42\n", true},
		{"host commands get only the host env", wd + "commands: {show: \"printf '%s,%s' \\\"$CUST\\\" \\\"$NEW\\\"\"}\n", "CUST=evil NEW=1 show", "cus_host,", true},
		{"host commands may ignore their input", wd + "commands: {quick: \"true\"}\n", "yes | head -c 1048576 | quick; echo rc=$?", "rc=0", true},
		{"host command bytes survive", wd + "commands: {bin: \"printf '\\\\377\\\\000\\\\001'\"}\n", "bin > bin.dat && base64 bin.dat", "/wAB\n", true},
		{"the workspace cannot be the folder", "workingDirectory: .\n", "echo x", "just-bash setup: workingDirectory " + workspace + " must not be", false},
		{"RocketClaw's directory cannot hold the folder", "workingDirectory: .rocketclaw/x\n", "echo x", "nor lie inside", false},
		{"listed target", listedTarget, "curl -sS " + listed.URL + "/", "listed", true},
		{"unlisted target", listedTarget, "curl -sS " + unlisted.URL + "/", "URL not in allow-list", false},
		{"auto refuses loopback", wd, "curl -sS " + listed.URL + "/", "private/loopback IP address blocked", false},
		{"wildcards are unsupported", wd + "allowedNetworkTargets: ['*.googleapis.com']\n", "echo x", "wildcards and CIDR ranges are not supported", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shell, ok, err := Shell(frontmatter(t, "shell: emulated\n"+tc.settings), workspace, runtimeDir, dir)
			require.NoError(t, err)
			require.True(t, ok)

			// The harness starts the wrapper with no arguments and writes the setup line, then the command.
			env := map[string]string{}

			for _, kv := range append(os.Environ(), "ROCKETCLAW_CONVERSATION_ID=c1", "CUST=cus_host", "TMPDIR="+tmp) {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}

			line, err := json.Marshal(map[string]any{"config": shell.Setup, "env": env})
			require.NoError(t, err)

			cmd := exec.CommandContext(t.Context(), shell.Path)
			cmd.Dir, cmd.Stdin = workspace, strings.NewReader(string(line)+"\n"+tc.command)

			out, err := cmd.CombinedOutput()
			require.Equal(t, tc.success, err == nil, string(out))
			require.Contains(t, string(out), tc.output)
		})
	}

	data, err := os.ReadFile(filepath.Join(folder, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(data))
}

func TestInstallFailure(t *testing.T) {
	t.Parallel()

	blocked := filepath.Join(t.TempDir(), "justbash")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))
	require.ErrorContains(t, Install(blocked), "install just-bash")

	var want struct{ Dependencies map[string]string }
	require.NoError(t, json.Unmarshal(packageJSON, &want))
	require.Regexp(t, `^\d+\.\d+\.\d+$`, want.Dependencies["just-bash"], "just-bash is pinned to an exact version")
}
