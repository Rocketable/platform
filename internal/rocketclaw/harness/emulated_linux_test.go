//go:build linux

package harness

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox/wire"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "rocketclaw-forge" {
		os.Exit(forge(os.Args[1:]))
	}

	coverDir := "/tmp"
	if _, err := os.Stat(".gocover"); err == nil {
		coverDir, _ = filepath.Abs(".gocover")
	}

	_ = os.Setenv("GOCOVERDIR", coverDir)

	if code, ok := sandbox.Guest(os.Args); ok {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func emulatedRuntime(t *testing.T, logs io.Writer) *sandbox.Runtime {
	t.Helper()

	dir := os.Getenv("ROCKETCLAW_SANDBOX_DIR")
	if dir == "" {
		if os.Getenv("ROCKETCLAW_SANDBOX_REQUIRE") == "1" {
			t.Fatal("ROCKETCLAW_SANDBOX_DIR is required")
		}

		t.Skip("WARNING: skipping an emulated shell acceptance test: no ROCKETCLAW_SANDBOX_DIR (CI fails here)")
	}

	stale := filepath.Join(dir, "rootfs-0000000000000000")
	require.NoError(t, os.MkdirAll(filepath.Join(stale, "usr"), 0o755))

	rt := sandbox.Bubblewrap{}.Prepare(t.Context(), dir, slog.New(slog.NewTextHandler(io.MultiWriter(t.Output(), logs), nil)))
	t.Cleanup(func() {
		_ = rt.Wait(context.WithoutCancel(t.Context()))
	})

	for rt.Err() == sandbox.ErrPreparing {
		time.Sleep(10 * time.Millisecond)
	}

	require.NoError(t, rt.Err())
	require.NoDirExists(t, stale)

	return rt
}

type emulated struct {
	sss   *sandboxedShellSystem
	shell CustomShell
	logs  *bytes.Buffer
}

func newEmulated(t *testing.T, commands, secrets map[string]string, targets []string, readOnly ...string) *emulated {
	t.Helper()

	logs := &bytes.Buffer{}
	rt := emulatedRuntime(t, logs)

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	if dst := os.Getenv("ROCKETCLAW_SANDBOX_GOCOVERDIR"); dst != "" {
		guest := filepath.Join(folder, ".gocover")
		require.NoError(t, os.Mkdir(guest, 0o755))
		t.Cleanup(func() {
			out, err := os.MkdirTemp(dst, "guest-")
			require.NoError(t, err)
			require.NoError(t, os.CopyFS(out, os.DirFS(guest)))
		})
	}

	require.NoError(t, root.Mkdir(".tmp", 0o700))
	shellTemp := testShellTempConfig(t, root, filepath.Join(workspace, ".tmp"))

	return &emulated{
		sss:   newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=web:c1", "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"}, rt),
		shell: CustomShell{Emulated: true, Dir: folder, ReadOnly: readOnly, Commands: commands, Secrets: secrets, NetworkTargets: targets},
		logs:  logs,
	}
}

func (e *emulated) bash(t *testing.T, command string) BashResult {
	t.Helper()

	return e.sss.Bash(t.Context(), &e.shell, &bashParams{Command: command, Workdir: e.shell.Dir})
}

func (e *emulated) sandboxed(t *testing.T, command string) BashResult {
	t.Helper()

	return emulatedBashResult(runEmulated(t.Context(), e.sss.sandbox, &e.shell, command, "", e.shell.Dir, e.sss.env, append(os.Environ(), e.sss.env...), defaultShellTimeout*time.Millisecond))
}

func (e *emulated) ok(t *testing.T, command, want string) {
	t.Helper()
	requireBashOutput(t, command, e.bash(t, command), want)
}

func (e *emulated) sandboxedOK(t *testing.T, command, want string) {
	t.Helper()
	requireBashOutput(t, command, e.sandboxed(t, command), want)
}

func requireBashOutput(t *testing.T, command string, got BashResult, want string) {
	t.Helper()
	require.True(t, got.Success, "%s: %s (%s)", command, got.Output, got.ErrorCode)
	require.Equal(t, want, got.Output, command)
}

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

	e.ok(t, "echo t > /tmp/t && cat /tmp/t", "t\n")
	e.ok(t, "cat /tmp/t 2>&1 || true", "cat: /tmp/t: No such file or directory\n")

	e.ok(t, "mkdir /tmp/d && touch /tmp/d/f && cp /tmp/d/f /tmp/g && rm /tmp/g && cd /tmp/d && ls && cat /tmp/d/f", "f\n")
	e.ok(t, "touch ~/h && cat ~/h && cd ~ && ls", "h\n")
	require.Equal(t, deniedBashOutsidePath, e.bash(t, "head -c 4 /dev/urandom").Output)
	require.Equal(t, deniedBashOutsidePath, e.bash(t, "grep -c ^Pid: /proc/self/status").Output)

	got = e.sss.Bash(t.Context(), &e.shell, &bashParams{Command: "pwd; touch here; ls", Workdir: "/tmp"})
	require.True(t, got.Success, got.Output)
	require.Equal(t, "/tmp\nhere\n", got.Output)
}

func TestEmulatedShellStdin(t *testing.T) {
	e := newEmulated(t, map[string]string{"count": "wc -c"}, nil, nil)
	stdin := func(command, text string, timeout int) BashResult {
		return e.sss.Bash(t.Context(), &e.shell, &bashParams{Command: command, Workdir: e.shell.Dir, TimeoutMillisecond: timeout, Stdin: bashStdin{text: text}})
	}

	piped := "\x00\x01\xfe\xff" + strings.Repeat("payload ", 1<<16)
	requireBashOutput(t, "cat > piped.bin", stdin("cat > piped.bin", piped, 0), "(no output)")

	data, err := os.ReadFile(filepath.Join(e.shell.Dir, "piped.bin"))
	require.NoError(t, err)
	require.Equal(t, piped, string(data))

	large := strings.Repeat("x", 17<<20)
	requireBashOutput(t, "wc -c", stdin("wc -c", large, 0), "17825792\n")
	requireBashOutput(t, "count", stdin("count", large, 0), "17825792\n")
	requireBashOutput(t, "true", stdin("true", large, 0), "(no output)")
	require.Equal(t, "timeout", stdin("sleep 30", large, 200).ErrorCode)
}

func TestEmulatedShellEnvironment(t *testing.T) {
	t.Setenv("ROCKETCLAW_HOST_ONLY", "host-process-secret")

	e := newEmulated(t, map[string]string{"quiet": "true"}, map[string]string{"AGENT_SECRET": "agent-secret"}, nil)

	got := e.sandboxed(t, "env; cat /proc/[0-9]*/environ /proc/[0-9]*/cmdline 2>/dev/null; ls -la /extensions /run/rocketclaw; cat /run/rocketclaw/script; quiet")
	require.True(t, got.Success, got.Output)
	require.Contains(t, got.Output, "ROCKETCLAW_CONVERSATION_ID=web:c1")

	for _, leak := range []string{"host-process-secret", "agent-secret", "HOME=" + os.Getenv("HOME") + "\n", e.sss.root.Name(), os.Getenv("ROCKETCLAW_SANDBOX_DIR")} {
		require.NotContains(t, got.Output, leak)
	}
}

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
		"drain":    "cat > /dev/null",
		"killed":   "kill -9 $$",
	}, nil, nil)

	e.ok(t, "echo hello | upper", "HELLO\n")

	got := e.bash(t, strings.Repeat(":", 8<<20+1))
	require.False(t, got.Success)
	require.Contains(t, got.Output, "command of 8388609 bytes exceeds 8388608")

	got = newEmulated(t, map[string]string{"rocketclaw-x": "true"}, nil, nil).bash(t, "true")
	require.False(t, got.Success)
	require.Contains(t, got.Output, `"rocketclaw-x": names are`)

	e.sandboxedOK(t, "head -c 17000000 /dev/zero | drain > /dev/null 2>&1; echo $?", "0\n")
	e.ok(t, "killed; echo $?", "137\n")

	e.ok(t, `coproc S { stream; }; echo abc >&"${S[1]}"; read -r l <&"${S[0]}"; echo "got $l"`, "got ABC\n")

	e.ok(t, `code17 || echo "failed $?"`, "failed 17\n")
	e.ok(t, `printf 'in' > in; upper < in > out 2> err; cat out; wc -c < err`, "IN0\n")
	e.ok(t, `args '' 'a b' 'ünïcødé' $'line\nbreak'`, "[][a b][ünïcødé][line\nbreak]")

	require.NoError(t, os.Mkdir(filepath.Join(e.shell.Dir, "sub"), 0o755))
	e.ok(t, "cd sub && where", filepath.Join(e.shell.Dir, "sub")+"\n")

	host, err := os.ReadFile("/proc/sys/kernel/hostname")
	require.NoError(t, err)
	e.sandboxedOK(t, "hostname; cat /proc/sys/kernel/hostname", string(host)+"rocketclaw\n")

	builtin := newEmulated(t, map[string]string{"echo": "printf 'host:%s\n'"}, nil, nil)
	builtin.ok(t, "echo hi; builtin echo native; command echo native2; /extensions/echo abs; bash -c 'echo nested'", "host:hi\nnative\nnative2\nhost:abs\nhost:nested\n")
	e.ok(t, "my-cmd", "dashed")

	e.sandboxedOK(t, "head -c 4000000 /dev/urandom > r; copy < r | copy | cmp - r && echo same", "same\n")

	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/evil && /tmp/evil; echo $?", "evil: command not found\n127\n")
	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge unknown", "exit 127: arbitrary: command not found\n")
	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge otherexec upper", "exit 126: rocketclaw: host command request for another command\n")
	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge huge; echo done", "exit 126: rocketclaw: malformed host command request\ndone\n")
	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge hangup copy; echo done", "done\n")
}

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

	e.ok(t, "PATH=/evil AWS_SECRET_ACCESS_KEY=evil HTTPS_PROXY=http://evil /extensions/probe", "host-aws http://host-proxy "+os.Getenv("PATH")+"\n")

	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge guestenv deploy; deploy --release v8", "exit 126: rocketclaw: malformed host command request\n"+want("v8"))
	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge hostenv deploy", "exit 126: rocketclaw: malformed host command request\n")

	got := e.sandboxed(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge raw quiet | od -c")
	require.True(t, got.Success, got.Output)
	require.NotContains(t, strings.ReplaceAll(got.Output, " ", ""), "host-secret")

	other := newEmulated(t, map[string]string{"deploy": `bash -c 'echo "[${DEPLOY_TOKEN-unset}]"'`}, map[string]string{"OTHER": "o"}, nil)
	other.ok(t, "deploy", "[unset]\n")
}

func forge(args []string) int {
	execID, _ := os.ReadFile("/run/rocketclaw/exec-id")

	conn, err := net.Dial("unix", "/run/rocketclaw/command.sock")
	if err != nil {
		fmt.Println("dial:", err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	start := &wire.InvokeStart{ExecId: execID}
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
	case "guestenv", "hostenv":
		field := protowire.Number(6)
		if args[0] == "guestenv" {
			field = 5
		}

		raw, _ := proto.Marshal(start)
		raw = protowire.AppendBytes(protowire.AppendTag(raw, field, protowire.BytesType), []byte("DEPLOY_TOKEN=evil"))
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
