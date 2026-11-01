package harness

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A workdir is found in the agent's folders whether it is given by name or by the real path of a linked one,
// and comes back as the name the sandbox mounts.
func TestGuestWorkdir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	folder, realSkills := filepath.Join(base, "folder"), filepath.Join(base, "real-skills")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(realSkills, "deep"), 0o755))

	skills := filepath.Join(base, "skills")
	require.NoError(t, os.Symlink(realSkills, skills))

	folders := []string{folder, skills}

	for _, tc := range []struct{ cwd, want string }{
		{folder, folder},
		{filepath.Join(folder, "sub"), filepath.Join(folder, "sub")},
		{filepath.Join(skills, "deep"), filepath.Join(skills, "deep")},
		{filepath.Join(realSkills, "deep"), filepath.Join(skills, "deep")},
	} {
		got, ok := guestWorkdir(folders, tc.cwd)
		require.True(t, ok, tc.cwd)
		require.Equal(t, tc.want, got)
	}

	for _, cwd := range []string{base, filepath.Join(base, "missing")} {
		_, ok := guestWorkdir(folders, cwd)
		require.False(t, ok, cwd)
	}
}

// curl takes -s -S -f -L -o, alone or combined, fetches through the client it is given, and writes -o only
// inside the folder; anything else fails rather than change the request.
func TestEmulatedCurl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/huge":
			_, _ = w.Write(make([]byte, maxCurlBytes+1))
		default:
			_, _ = w.Write([]byte{'o', 'k', 0, 0xff})
		}
	}))
	t.Cleanup(server.Close)

	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(folder, "link")))

	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer

		code := curl(t.Context(), server.Client(), folder, args, folder, &stdout, &stderr)

		return code, stdout.String(), stderr.String()
	}

	for _, args := range [][]string{{server.URL + "/"}, {"-fsSL", server.URL + "/"}, {"-s", "-S", "--fail", "--location", server.URL + "/"}, {"--", server.URL + "/"}} {
		code, out, _ := run(args...)
		require.Equal(t, 0, code, args)
		require.Equal(t, "ok\x00\xff", out, "bytes come back exactly")
	}

	for _, args := range [][]string{{"-so", "a", server.URL + "/"}, {"-soa", server.URL + "/"}, {"-o", "a", server.URL + "/"}, {"--output", filepath.Join(folder, "a"), server.URL + "/"}} {
		code, out, errOut := run(args...)
		require.Equal(t, 0, code, "%v: %s", args, errOut)
		require.Empty(t, out)

		data, err := os.ReadFile(filepath.Join(folder, "a"))
		require.NoError(t, err)
		require.Equal(t, "ok\x00\xff", string(data))
	}

	for _, tc := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"-X", "POST", server.URL + "/"}, 2, "option -X is not supported"},
		{[]string{"-d", "x", server.URL + "/"}, 2, "option -d is not supported"},
		{[]string{"--data-raw", "x", server.URL + "/"}, 2, "option --data-raw is not supported"},
		{[]string{server.URL + "/", server.URL + "/"}, 2, "one URL only"},
		{[]string{"-s"}, 2, "no URL specified"},
		{[]string{"-o"}, 2, "option -o needs a file"},
		{[]string{"-so"}, 2, "option -o needs a file"},
		{[]string{"-f", server.URL + "/missing"}, 22, "returned error: 404"},
		{[]string{server.URL + "/huge"}, 63, "exceeds 10 MiB"},
		{[]string{"-o", "link/outside", server.URL + "/"}, 23, ""},
		{[]string{"-o", "../escape", server.URL + "/"}, 23, "outside the agent's folder"},
		{[]string{"-o", outside, server.URL + "/"}, 23, "outside the agent's folder"},
		{[]string{"http://127.0.0.1:1/"}, 7, ""},
	} {
		code, _, errOut := run(tc.args...)
		require.Equal(t, tc.code, code, "%v: %s", tc.args, errOut)
		require.Contains(t, errOut, tc.msg, tc.args)
	}

	require.NoFileExists(t, outside)

	code, _, errOut := run("-s", "-f", server.URL+"/missing")
	require.Equal(t, 22, code)
	require.Empty(t, errOut, "-s hides the error")

	_, _, errOut = run("-sS", "-f", server.URL+"/missing")
	require.Contains(t, errOut, "returned error: 404", "-S shows it again")

	code, out, _ := run("example.invalid")
	require.Equal(t, 7, code, "a URL without a scheme is http: %s", out)
}

// An agent's command runs its line in host bash with the guest's arguments, the host environment and the
// agent's secrets, in the guest's directory when it is really inside the folder.
func TestEmulatedHostCommand(t *testing.T) {
	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(folder, "sub"), 0o755))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(folder, "away")))

	shell := &CustomShell{Dir: folder, Commands: map[string]string{
		"show":  `printf '%s|' "$TOKEN" "$PWD"`,
		"code":  "exit",
		"upper": "tr a-z A-Z",
		"kill":  "kill -TERM $$",
	}}
	host := &emulatedHost{shell: shell, env: []string{"PATH=" + os.Getenv("PATH"), "TOKEN=host-secret"}}

	run := func(name, cwd, stdin string, args ...string) (int, string) {
		var out bytes.Buffer

		code := host.Command(t.Context(), name, args, cwd, strings.NewReader(stdin), &out, &out)

		return code, out.String()
	}

	code, out := run("show", filepath.Join(folder, "sub"), "", "a b")
	require.Equal(t, 0, code)
	require.Equal(t, "host-secret|"+filepath.Join(folder, "sub")+"|a b|", out)

	_, out = run("show", filepath.Join(folder, "away"), "")
	require.Equal(t, "host-secret|"+folder+"|", out, "a link out of the folder falls back to the folder")

	_, out = run("show", "/", "")
	require.Equal(t, "host-secret|"+folder+"|", out)

	code, _ = run("code", folder, "", "17")
	require.Equal(t, 17, code)

	_, out = run("upper", folder, "hello")
	require.Equal(t, "HELLO", out)

	code, _ = run("kill", folder, "")
	require.Equal(t, 128+15, code, "a signal is reported as a shell does")

	host.shell = &CustomShell{Dir: filepath.Join(folder, "missing"), Commands: shell.Commands}
	code, out = run("show", folder, "")
	require.Equal(t, 126, code, "a host bash that cannot start reports 126: %s", out)
}
