package justbash

import (
	"cmp"
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

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

func frontmatter(t *testing.T, doc string) map[string]any {
	t.Helper()

	var fm map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &fm))

	return fm
}

func TestShell(t *testing.T) {
	t.Parallel()

	shell, err := Shell(frontmatter(t, "model: x\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
	require.False(t, shell.Emulated)

	shell, err = Shell(frontmatter(t, "shell: /bin/sandbox\nworkingDirectory: data\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
	require.False(t, shell.Emulated, "a program path is not the emulated shell")

	_, err = Shell(frontmatter(t, "shell: emulated\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.EqualError(t, err, "workingDirectory: required with shell: emulated")

	shell, err = Shell(frontmatter(t, "shell: emulated\nmodel: x\nworkingDirectory: data\ncommands: {hello: printf hi}\nallowedNetworkTargets: [api.stripe.com]\n"), "/ws", "/ws/.rocketclaw", "/ws/.rocketclaw/justbash")
	require.NoError(t, err)
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

	unlisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("unlisted")) }))
	t.Cleanup(unlisted.Close)

	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, unlisted.URL+"/", http.StatusFound)

			return
		}

		_, _ = w.Write([]byte("listed"))
	}))
	t.Cleanup(listed.Close)

	folder, wd := filepath.Join(workspace, "justbash", "c1"), "workingDirectory: justbash/$ROCKETCLAW_CONVERSATION_ID\n"

	// An easy agent's folder comes resolved, here outside the workspace, and is used as written.
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	easyFolder := filepath.Join(outside, "$ROCKETCLAW_CONVERSATION_ID")
	require.NoError(t, os.Mkdir(easyFolder, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(easyFolder, "file.txt"), []byte("easy"), 0o600))

	// The chat's uploads and the agent's skills show read-only at their real paths.
	upload, skill := filepath.Join(runtimeDir, "uploads", "c1", "up1", "report.txt"), filepath.Join(runtimeDir, "skills", "tool")
	require.NoError(t, os.MkdirAll(filepath.Dir(upload), 0o700))
	require.NoError(t, os.WriteFile(upload, []byte("uploaded"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(skill, "scripts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "scripts", "run.sh"), []byte("echo \"skill ran in $(pwd)\"\n"), 0o600))

	turn := map[string]string{"ROCKETCLAW_CONVERSATION_ID": "c1"}
	easy := &Setup{Folder: easyFolder, Env: turn}
	mounted := &Setup{Folder: easyFolder, Env: turn, ReadOnly: []string{filepath.Join(runtimeDir, "uploads", "c1"), skill}}
	secrets := &Setup{Folder: easyFolder, Env: turn, Commands: map[string]string{"show": "printenv SECRET_KEY"}, Secrets: map[string]string{"SECRET_KEY": "s3cr3t"}}

	cases := []struct {
		name, settings, command, output string
		success                         bool
		// setup makes the case an easy agent's, which has no settings.
		setup *Setup
	}{
		{"an easy emulator sees only the turn's variables and just-bash's own", "", "printenv", "HOME=/\nPATH=/usr/bin:/bin\nIFS= \t\n\nOSTYPE=linux-gnu\nMACHTYPE=x86_64-pc-linux-gnu\nHOSTTYPE=x86_64\nHOSTNAME=localhost\nPWD=" + easyFolder + "\nOLDPWD=" + easyFolder + "\nOPTIND=1\nROCKETCLAW_CONVERSATION_ID=c1\nTMPDIR=/tmp\nSHELLOPTS=braceexpand:hashall:interactive-comments\nBASHOPTS=globskipdots\n", true, easy},
		{"easy commands get secrets the emulator never sees", "", `show && echo "[$SECRET_KEY]"`, "s3cr3t\n[]\n", true, secrets},
		{"an easy agent without commands has no host commands", "", "show", "command not found", false, easy},
		{"an easy folder is used as given", "", "cat '" + filepath.Join(easyFolder, "file.txt") + "' && echo && pwd", "easy\n" + easyFolder + "\n", true, easy},
		{"host files are invisible to an easy agent", "", "cat /etc/hosts", "No such file or directory", false, easy},
		{"an upload reads at its real path", "", "cat " + upload, "uploaded", true, mounted},
		{"an upload cannot change", "", "echo x > " + upload, "EROFS: read-only file system, write '/up1/report.txt'\n", false, mounted},
		{"a skill script runs from its read-only folder", "", "bash " + filepath.Join(skill, "scripts", "run.sh"), "skill ran in " + easyFolder + "\n", true, mounted},
		{"writes land in the folder, TMPDIR stays in memory and interpreters run", wd, `echo hi > f.txt && cat f.txt && pwd && f=$(mktemp) && echo t > $f && cat $f && echo $TMPDIR && python3 -c "print(6*7)" && js-exec -c "console.log(6*7)"`, "hi\n" + folder + "\nt\n/tmp\n42\n42\n", true, nil},
		{"files outside the folder are invisible", wd, "cat " + filepath.Join(workspace, "outside.txt"), "No such file or directory", false, nil},
		{"the host TMPDIR is invisible", wd, "ls " + tmp, "No such file or directory", false, nil},
		{"host commands keep the host env and binary output", wd + "commands: {show: \"printf '%s,%s' \\\"$CUST\\\" \\\"$NEW\\\"\", bin: \"printf '\\\\377\\\\000\\\\001'\"}\n", "CUST=evil NEW=1 show && printf '\\n' && bin > bin.dat && base64 bin.dat", "cus_host,\n/wAB\n", true, nil},
		{"host commands may ignore their input", wd + "commands: {quick: \"true\"}\n", "yes | head -c 1048576 | quick; echo rc=$?", "rc=0", true, nil},
		{"the workspace cannot be the folder", "workingDirectory: .\n", "echo x", "just-bash setup: workingDirectory " + workspace + " must not be", false, nil},
		{"RocketClaw's directory cannot hold the folder", "workingDirectory: .rocketclaw/x\n", "echo x", "nor lie inside", false, nil},
		{"wildcards are unsupported", wd + "allowedNetworkTargets: ['*.googleapis.com']\n", "echo x", "wildcards and CIDR ranges are not supported", false, nil},
	}

	// run runs command in shell as the harness does: it starts the wrapper with no arguments in workdir, by
	// default an easy agent's folder or else the workspace, and writes the setup line, then the command. The
	// process env may hold RocketClaw variables of no turn.
	run := func(t *testing.T, shell harness.CustomShell, workdir, command string) (string, error) {
		t.Helper()

		env := map[string]string{}

		for _, kv := range append(os.Environ(), "ROCKETCLAW_CONVERSATION_ID=c1", "ROCKETCLAW_TEST_DATABASE_URL=postgres://leak", "CUST=cus_host", "TMPDIR="+tmp) {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}

		delete(env, "NEW")

		line, err := json.Marshal(map[string]any{"config": shell.Setup, "env": env})
		require.NoError(t, err)

		cmd := exec.CommandContext(t.Context(), shell.Path)
		cmd.Dir, cmd.Stdin = cmp.Or(workdir, shell.Dir, workspace), strings.NewReader(string(line)+"\n"+command)

		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var shell harness.CustomShell
			if tc.setup != nil {
				shell = EasyShell(tc.setup, dir)
			} else {
				advanced, err := Shell(frontmatter(t, "shell: emulated\n"+tc.settings), workspace, runtimeDir, dir)
				require.NoError(t, err)
				require.True(t, advanced.Emulated)

				shell = advanced
			}

			out, err := run(t, shell, "", tc.command)
			require.Equal(t, tc.success, err == nil, out)
			require.Contains(t, out, tc.output)
		})
	}

	// A read-only folder named through a symlink, as under a symlinked workspace, mounts where it is named,
	// though the OS reports its real path as the working directory.
	linked := filepath.Join(outside, "linked")
	require.NoError(t, os.Symlink(skill, linked))

	// An easy agent's workdir in a read-only folder is honored; one outside its folders fails rather than run elsewhere.
	for _, tc := range []struct {
		name, workdir, command, output string
		success                        bool
		setup                          *Setup
	}{
		{"a workdir in a read-only folder is used", skill, "pwd && cat scripts/run.sh", skill + "\necho \"skill ran in $(pwd)\"\n", true, mounted},
		{"a workdir in a read-only folder stays read-only", skill, "echo x > new.txt", "EROFS: read-only file system", false, mounted},
		{"a workdir outside the folders fails", workspace, "pwd", "just-bash setup: workdir " + workspace + " is outside this agent's folders\n", false, mounted},
		{"a workdir in a symlinked read-only folder is used", filepath.Join(linked, "scripts"), "pwd && cat run.sh", filepath.Join(linked, "scripts") + "\necho \"skill ran in $(pwd)\"\n", true, &Setup{Folder: easyFolder, Env: turn, ReadOnly: []string{linked}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, EasyShell(tc.setup, dir), tc.workdir, tc.command)
			require.Equal(t, tc.success, err == nil, out)
			require.Contains(t, out, tc.output)
		})
	}

	// curl refuses exactly the targets an easy agent's webfetch refuses, by the table both tests share.
	data, err := os.ReadFile(filepath.Join("testdata", "network_targets.json"))
	require.NoError(t, err)

	var rows []struct {
		Targets json.RawMessage
		URL     string
		Allow   bool
	}

	hosts := strings.NewReplacer("{listed}", strings.TrimPrefix(listed.URL, "http://"), "{unlisted}", strings.TrimPrefix(unlisted.URL, "http://"))
	require.NoError(t, json.Unmarshal([]byte(hosts.Replace(string(data))), &rows))

	for _, row := range rows {
		t.Run(string(row.Targets)+" "+row.URL, func(t *testing.T) {
			setup := &Setup{Folder: easyFolder, Env: turn}
			if string(row.Targets) != `"auto"` {
				require.NoError(t, json.Unmarshal(row.Targets, &setup.NetworkTargets))
			}

			out, _ := run(t, EasyShell(setup, dir), "", "curl -sSL '"+row.URL+"'")
			require.Equal(t, !row.Allow, strings.Contains(out, "Network access denied") || strings.Contains(out, "Redirect target not in allow-list"), out)
		})
	}

	data, err = os.ReadFile(filepath.Join(folder, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(data))

	data, err = os.ReadFile(upload)
	require.NoError(t, err)
	require.Equal(t, "uploaded", string(data))
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
