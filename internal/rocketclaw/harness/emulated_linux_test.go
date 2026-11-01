//go:build linux

package harness

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox/wire"
)

// Acceptance tests of the emulated shell on real Linux (docs/specs/2026-11-01-linux-native-just-bash-spec.md
// §13), through the bash tool as agents use it. They need:
//
//	ROCKETCLAW_SANDBOX_ROOTFS   the zstd rootfs go generate builds (cmd/rocketclaw/sandbox-rootfs-$GOARCH.tar.zst)
//	ROCKETCLAW_SANDBOX_DIR      where to unpack it, on a local filesystem
//	ROCKETCLAW_SANDBOX_REQUIRE  1 to fail instead of skip without them (CI)
//
// and a test process in its own delegated cgroup (make -C internal/rocketclaw sandbox-test).

// TestMain makes this test binary act as RocketClaw does inside a sandbox: the guest relay, plus a forger
// that plays a hostile guest program talking to the broker directly.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "rocketclaw-forge" {
		os.Exit(forge(os.Args[1:]))
	}

	// A coverage build warns at exit without GOCOVERDIR; the guest's own coverage goes to its /tmp, unread.
	_ = os.Setenv("GOCOVERDIR", "/tmp")

	if code, ok := sandbox.Guest(os.Args); ok {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

// emulatedRuntime prepares the sandbox, as the backend does once per process: the unpacked rootfs is kept,
// and the test process moves into its cgroup.
func emulatedRuntime(t *testing.T) *sandbox.Runtime {
	t.Helper()

	path, dir := os.Getenv("ROCKETCLAW_SANDBOX_ROOTFS"), os.Getenv("ROCKETCLAW_SANDBOX_DIR")
	if path == "" || dir == "" {
		if os.Getenv("ROCKETCLAW_SANDBOX_REQUIRE") == "1" {
			t.Fatal("ROCKETCLAW_SANDBOX_ROOTFS and ROCKETCLAW_SANDBOX_DIR are required")
		}

		t.Skip("WARNING: skipping an emulated shell acceptance test: no sandbox rootfs (CI fails here)")
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	rt := sandbox.Prepare(t.Context(), data, dir)
	require.NoError(t, rt.Err())

	return rt
}

// emulated is one agent's emulated shell in a fresh folder, as setEasyShell sets it up for a turn.
type emulated struct {
	sss   *sandboxedShellSystem
	shell CustomShell
}

func newEmulated(t *testing.T, commands, secrets map[string]string, targets []string, readOnly ...string) *emulated {
	t.Helper()

	rt := emulatedRuntime(t)

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, root.Mkdir(".tmp", 0o700))
	shellTemp := testShellTempConfig(t, root, filepath.Join(workspace, ".tmp"))

	return &emulated{
		sss:   newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=web:c1"}, DefaultShellCommand{}, rt),
		shell: CustomShell{Emulated: true, Dir: folder, ReadOnly: readOnly, Commands: commands, Secrets: secrets, NetworkTargets: targets},
	}
}

func (e *emulated) bash(t *testing.T, command string) BashResult {
	t.Helper()

	return e.sss.Bash(t.Context(), &e.shell, bashParams{Command: command, Workdir: e.shell.Dir})
}

// ok runs command and checks it succeeded with output want.
func (e *emulated) ok(t *testing.T, command, want string) {
	t.Helper()

	got := e.bash(t, command)
	require.True(t, got.Success, "%s: %s (%s)", command, got.Output, got.ErrorCode)
	require.Equal(t, want, got.Output, command)
}

// A: real Bash, fresh state per command, files persisting in the agent's folder.
func TestEmulatedShellSession(t *testing.T) {
	e := newEmulated(t, nil, nil, nil)

	e.ok(t, "echo hello", "hello\n")

	got := e.bash(t, "false")
	require.Equal(t, "1", got.ErrorCode, "A02: a failing command is a result, not an infrastructure error")

	e.ok(t, "echo data > x", "(no output)")
	e.ok(t, "cat x", "data\n")
	data, err := os.ReadFile(filepath.Join(e.shell.Dir, "x"))
	require.NoError(t, err)
	require.Equal(t, "data\n", string(data), "A03: the folder is the agent's real folder")

	e.ok(t, "export TOKEN=secret; cd /tmp; f() { :; }; umask 077", "(no output)")
	e.ok(t, `echo ${TOKEN-unset}; pwd; type f >/dev/null 2>&1 || echo nof; umask`, "unset\n"+e.shell.Dir+"\nnof\n0022\n")

	e.ok(t, "printf 'hello' | tr a-z A-Z > out && cat out", "HELLO")

	e.ok(t, `
f() { local n=$1; if (( n > 1 )); then echo "$n $(f $((n-1)))"; else echo 1; fi; }
for i in 1 2 3; do echo "line $i"; done > loop.txt
while read -r l; do case $l in *2) echo "two: $l";; esac; done < loop.txt
f 3
x=$(echo sub | tr a-z A-Z); [[ $x == SUB ]] && echo "ok $x"
declare -A m=([k]=v); echo "${m[k]}"`, "two: line 2\n3 2 1\nok SUB\nv\n")

	e.ok(t, `printf '\x00\x01\xfe\xff' > bin; od -An -tx1 bin`, " 00 01 fe ff\n")

	// /tmp is the command's own: it works and never persists.
	e.ok(t, "echo t > /tmp/t && cat /tmp/t", "t\n")
	e.ok(t, "cat /tmp/t 2>&1 || true", "cat: /tmp/t: No such file or directory\n")
}

// A07, G08: the guest sees the turn's variables and nothing of RocketClaw's environment.
func TestEmulatedShellEnvironment(t *testing.T) {
	t.Setenv("ROCKETCLAW_HOST_ONLY", "host-process-secret")

	e := newEmulated(t, map[string]string{"quiet": "true"}, map[string]string{"AGENT_SECRET": "agent-secret"}, nil)

	got := e.bash(t, "env; cat /proc/[0-9]*/environ /proc/[0-9]*/cmdline 2>/dev/null; ls -la /extensions /run/rocketclaw; cat /run/rocketclaw/script; quiet")
	require.True(t, got.Success, got.Output)
	require.Contains(t, got.Output, "ROCKETCLAW_CONVERSATION_ID=web:c1")

	// The folder shows at its host path by design (R30); nothing else of the host does.
	for _, leak := range []string{"host-process-secret", "agent-secret", "HOME=" + os.Getenv("HOME") + "\n", e.sss.root.Name(), os.Getenv("ROCKETCLAW_SANDBOX_DIR")} {
		require.NotContains(t, got.Output, leak)
	}
}

// B: an agent's commands run on the host, in pipes, redirections and substitutions, byte-exact.
func TestEmulatedShellHostCommands(t *testing.T) {
	e := newEmulated(t, map[string]string{
		"upper":    "tr a-z A-Z",
		"stream":   "sed -u y/abc/ABC/",
		"code17":   "sh -c 'exit 17'",
		"args":     "printf '[%s]'",
		"where":    "pwd",
		"hostname": "cat /proc/sys/kernel/hostname",
		"my-cmd":   "printf dashed",
		"copy":     "cat",
	}, nil, nil)

	e.ok(t, "echo hello | upper", "HELLO\n")

	// B02: output streams back before stdin ends.
	e.ok(t, `coproc S { stream; }; echo abc >&"${S[1]}"; read -r l <&"${S[0]}"; echo "got $l"`, "got ABC\n")

	e.ok(t, `code17 || echo "failed $?"`, "failed 17\n")
	e.ok(t, `printf 'in' > in; upper < in > out 2> err; cat out; wc -c < err`, "IN0\n")
	e.ok(t, `args '' 'a b' 'ünïcødé' $'line\nbreak'`, "[][a b][ünïcødé][line\nbreak]")

	require.NoError(t, os.Mkdir(filepath.Join(e.shell.Dir, "sub"), 0o755))
	e.ok(t, "cd sub && where", filepath.Join(e.shell.Dir, "sub")+"\n")

	host, err := os.ReadFile("/proc/sys/kernel/hostname")
	require.NoError(t, err)
	e.ok(t, "hostname; cat /proc/sys/kernel/hostname", string(host)+"rocketclaw\n")

	// B08: a command named like a builtin wins for a bare name, in nested shells too; builtin and command
	// reach Bash's.
	builtin := newEmulated(t, map[string]string{"echo": "printf 'host:%s\n'"}, nil, nil)
	builtin.ok(t, "echo hi; builtin echo native; command echo native2; /extensions/echo abs; bash -c 'echo nested'", "host:hi\nnative\nnative2\nhost:abs\nhost:nested\n")
	e.ok(t, "my-cmd", "dashed")

	// B09, F05: two host commands in a pipeline, far past pipe capacity, byte-exact.
	e.ok(t, "head -c 4000000 /dev/urandom > r; copy < r | copy | cmp - r && echo same", "same\n")

	// B06, B07: unknown names and forged requests reach no host command.
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/evil && /tmp/evil; echo $?", "evil: command not found\n127\n")
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge unknown", "exit 127: arbitrary: command not found\n")
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge otherexec upper", "exit 126: rocketclaw: host command request for another command\n")
	// F02: a 2 GiB length is refused from the header, before the broker reads or allocates it.
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge huge; echo done", "exit 126: rocketclaw: malformed host command request\ndone\n")
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge hangup copy; echo done", "done\n")
}

// G: a command's secrets come from the agent, never the guest, whatever the script does; another agent's
// commands never see them; nothing of them reaches the guest but what the command prints.
func TestEmulatedShellSecrets(t *testing.T) {
	secrets := map[string]string{
		"DEPLOY_TOKEN": "host-secret", "DEPLOY_URL": "https://prod.example.test", "AWS_SECRET_ACCESS_KEY": "host-aws", "HTTPS_PROXY": "http://host-proxy",
	}
	e := newEmulated(t, map[string]string{
		"deploy": `bash -c 'echo "$DEPLOY_TOKEN $DEPLOY_URL $*"' deploy`,
		"probe":  `bash -c 'echo "$AWS_SECRET_ACCESS_KEY $HTTPS_PROXY $PATH"'`,
		"quiet":  "true",
	}, secrets, nil)

	want := func(release string) string {
		return "host-secret https://prod.example.test --release " + release + "\n"
	}

	e.ok(t, "DEPLOY_TOKEN=evil deploy --release v1", want("v1"))
	e.ok(t, "export DEPLOY_TOKEN=evil DEPLOY_URL=https://attacker.test; deploy --release v2", want("v2"))
	e.ok(t, "env DEPLOY_TOKEN=evil deploy --release v3", want("v3"))
	e.ok(t, "env -i DEPLOY_TOKEN=evil /extensions/deploy --release v4", want("v4"))
	e.ok(t, `bash -c 'export DEPLOY_TOKEN=x; deploy --release v5'; x=$(DEPLOY_TOKEN=y deploy --release v6); echo "$x"`, want("v5")+want("v6"))
	e.ok(t, "PATH=/evil:$PATH BASH_ENV=/tmp/x DEPLOY_TOKEN=z deploy --release v7", want("v7"))

	// G10: the host command's PATH, credentials and proxy are the host's, whatever the guest sets.
	e.ok(t, "PATH=/evil AWS_SECRET_ACCESS_KEY=evil HTTPS_PROXY=http://evil /extensions/probe", "host-aws http://host-proxy "+os.Getenv("PATH")+"\n")

	// G05: a forged request's guest environment changes nothing; an invented host environment field is refused.
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge guestenv deploy", "host-secret https://prod.example.test \nexit 0\n")
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge hostenv deploy", "exit 126: rocketclaw: malformed host command request\n")

	// G08: no frame a quiet command sends back carries a secret.
	got := e.bash(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge raw quiet | od -c")
	require.True(t, got.Success, got.Output)
	require.NotContains(t, strings.ReplaceAll(got.Output, " ", ""), "host-secret")

	// G07: another agent's commands have their own secrets only.
	other := newEmulated(t, map[string]string{"deploy": `bash -c 'echo "[${DEPLOY_TOKEN-unset}]"'`}, map[string]string{"OTHER": "o"}, nil)
	other.ok(t, "deploy", "[unset]\n")
}

// forge plays a hostile guest program: it talks to the broker without the relay and prints what it learns.
func forge(args []string) int {
	execID, _ := os.ReadFile("/run/rocketclaw/exec-id")

	conn, err := net.Dial("unix", "/run/rocketclaw/command.sock")
	if err != nil {
		fmt.Println("dial:", err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	start := &wire.InvokeStart{ExecId: execID, GuestEnv: [][]byte{[]byte("DEPLOY_TOKEN=evil")}}
	if len(args) > 1 {
		start.Name = []byte(args[1])
	}

	w := wire.NewWriter(conn)
	frame := &wire.Frame{Body: &wire.Frame_InvokeStart{InvokeStart: start}}

	switch args[0] {
	case "unknown":
		start.Name = []byte("arbitrary")
	case "otherexec":
		start.ExecId = []byte("x-forged")
	case "hostenv":
		raw, _ := proto.Marshal(start)
		raw = protowire.AppendBytes(protowire.AppendTag(raw, 6, protowire.BytesType), []byte("DEPLOY_TOKEN=evil"))
		inner := protowire.AppendBytes(protowire.AppendTag(protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), wire.Version), 2, protowire.BytesType), raw)
		_, _ = conn.Write(append(binary.AppendUvarint(nil, uint64(len(inner))), inner...))
		frame = nil
	case "huge":
		_, _ = conn.Write(binary.AppendUvarint(nil, 1<<31))
		frame = nil
	case "hangup":
		_ = w.Write(frame)
		return 0
	case "dial":
		if _, err := net.Dial("tcp", args[1]); err != nil {
			fmt.Println("dial:", err)
			return 0
		}

		fmt.Println("connected")

		return 0
	case "raw":
		_ = w.Write(frame)
		_ = w.Write(&wire.Frame{Body: &wire.Frame_StdinEof{StdinEof: &wire.StdinEOF{}}})

		var all bytes.Buffer

		_, _ = all.ReadFrom(conn)
		_, _ = os.Stdout.Write(all.Bytes())

		return 0
	}

	if frame != nil {
		_ = w.Write(frame)
		_ = w.Write(&wire.Frame{Body: &wire.Frame_StdinEof{StdinEof: &wire.StdinEOF{}}})
	}

	r := wire.NewReader(conn)
	for {
		f, err := r.Read()
		if err != nil {
			fmt.Println("closed")
			return 0
		}

		switch body := f.GetBody().(type) {
		case *wire.Frame_StdoutChunk:
			_, _ = os.Stdout.Write(body.StdoutChunk)
		case *wire.Frame_InvokeExit:
			line := "exit " + strconv.Itoa(int(body.InvokeExit.GetCode()))
			if msg := strings.TrimSpace(string(body.InvokeExit.GetMessage())); msg != "" {
				line += ": " + msg
			}

			fmt.Println(line)

			return 0
		}
	}
}
