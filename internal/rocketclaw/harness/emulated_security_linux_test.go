//go:build linux

package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmulatedShellNetwork(t *testing.T) {
	unlisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("unlisted")) }))
	t.Cleanup(unlisted.Close)

	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, unlisted.URL+"/", http.StatusFound)
		case "/huge":
			_, _ = w.Write(make([]byte, maxCurlBytes+1))
		case "/binary":
			_, _ = w.Write([]byte{0, 1, 0xfe, 0xff})
		default:
			_, _ = w.Write([]byte("listed"))
		}
	}))
	t.Cleanup(listed.Close)

	data, err := os.ReadFile(filepath.Join("testdata", "network_targets.json"))
	require.NoError(t, err)

	var rows []struct {
		Targets json.RawMessage
		URL     string
		Allow   bool
	}

	listedHost, unlistedHost := strings.TrimPrefix(listed.URL, "http://"), strings.TrimPrefix(unlisted.URL, "http://")
	require.NoError(t, json.Unmarshal([]byte(strings.NewReplacer("{listed}", listedHost, "{unlisted}", unlistedHost).Replace(string(data))), &rows))

	for _, row := range rows {
		t.Run("C02-C09 "+string(row.Targets)+" "+row.URL, func(t *testing.T) {
			var targets []string
			if string(row.Targets) != `"auto"` {
				require.NoError(t, json.Unmarshal(row.Targets, &targets))
			}

			got := newEmulated(t, nil, nil, targets).bash(t, "curl -sS "+row.URL)
			require.Equal(t, !row.Allow, strings.Contains(got.Output, "not in allowedNetworkTargets"), got.Output)

			if !row.Allow {
				require.Equal(t, "7", got.ErrorCode, got.Output)
			}
		})
	}

	e := newEmulated(t, map[string]string{"hostfetch": "curl -s " + unlisted.URL + "/"}, nil, []string{listedHost})

	e.ok(t, "curl -fsSL "+listed.URL+"/binary | od -An -tx1", " 00 01 fe ff\n")
	e.ok(t, "curl -so page "+listed.URL+"/ && cat page", "listed")

	got := e.bash(t, "curl -s "+listed.URL+"/huge > /dev/null")
	require.Equal(t, "63", got.ErrorCode, "C11: a response past the limit stops")

	got = e.bash(t, "curl -X POST -d x "+listed.URL+"/")
	require.Equal(t, "2", got.ErrorCode, "methods other than GET fail rather than change the request: %s", got.Output)

	_, port, _ := strings.Cut(listedHost, ":")
	for _, dial := range []string{
		`python3 -c 'import socket; socket.create_connection(("1.1.1.1", 443), 1)'`,
		`python3 -c 'import socket; socket.create_connection(("127.0.0.1", ` + port + `), 1)'`,
		`python3 -c 'import socket; s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b"x", ("1.1.1.1", 53))'`,
		`python3 -c 'import socket; socket.socket(socket.AF_PACKET, socket.SOCK_RAW)'`,
		"wget -q -T 2 -O- " + listed.URL + "/",
		`node -e 'fetch("` + listed.URL + `/").then(() => process.exit(0), () => process.exit(1))'`,
		"exec 3<>/dev/tcp/1.1.1.1/443",
	} {
		got := e.bash(t, dial)
		require.False(t, got.Success, "%s reached the network: %s", dial, got.Output)
	}

	e.sandboxedOK(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge dial 1.1.1.1:443 | cut -c1-5", "dial:\n")

	e.ok(t, "hostfetch", "unlisted")
	require.Contains(t, e.bash(t, "curl -sS "+unlisted.URL+"/").Output, "not in allowedNetworkTargets")
}

func TestEmulatedShellFilesystem(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "host-marker")
	require.NoError(t, os.WriteFile(marker, []byte("host"), 0o644))

	uploads, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(uploads, "report.txt"), []byte("upload"), 0o644))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("evil")) }))
	t.Cleanup(server.Close)

	e := newEmulated(t, map[string]string{"where": "pwd"}, nil, []string{strings.TrimPrefix(server.URL, "http://")}, uploads)
	other := newEmulated(t, nil, nil, nil)
	require.NoError(t, os.WriteFile(filepath.Join(other.shell.Dir, "theirs"), []byte("other agent"), 0o644))

	for _, path := range []string{marker, filepath.Join(other.shell.Dir, "theirs"), "/etc/shadow", "/var/run/docker.sock", "/run/dbus/system_bus_socket", "/sys/kernel"} {
		require.Equal(t, deniedBashOutsidePath, e.bash(t, "cat "+path).Output, path)

		got := e.sandboxed(t, "cat "+path)
		require.False(t, got.Success, "D01, D04, D07: %s is visible: %s", path, got.Output)
	}

	bashrc := filepath.Join(os.Getenv("HOME"), ".bashrc")
	if host, err := os.ReadFile(bashrc); err == nil {
		require.NotEqual(t, string(host), e.sandboxed(t, "cat "+bashrc).Output, "D01: the host's %s is visible", bashrc)
	}

	e.ok(t, "cat "+filepath.Join(uploads, "report.txt"), "upload")
	require.False(t, e.bash(t, "echo x > "+filepath.Join(uploads, "new")).Success, "uploads are read-only")
	require.False(t, e.sandboxed(t, "touch /usr/bin/evil || touch /evil || touch /etc/evil").Success, "D02: the rootfs is read-only")

	require.False(t, e.sandboxed(t, "ln -s "+filepath.Dir(marker)+" out && cd out").Success, "a link out of the folder leads nowhere in the guest")

	for _, target := range []string{"out/host-marker", "../escape", marker, "/etc/passwd"} {
		got := e.bash(t, "curl -so "+target+" "+server.URL+"/")
		require.Equal(t, "23", got.ErrorCode, "curl -o %s wrote outside the folder: %s", target, got.Output)
	}

	e.ok(t, "curl -so inside "+server.URL+"/ && cat inside", "evil")

	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "host", string(data))
	require.NoFileExists(t, filepath.Join(filepath.Dir(e.shell.Dir), "escape"))

	got := e.sandboxed(t, "head -c 300M /dev/zero > big")
	require.False(t, got.Success, got.Output)
	require.Regexp(t, `File size limit exceeded|memory \(256 MiB\) or process \(64\) limit reached`, got.Output)

	big, err := os.Stat(filepath.Join(e.shell.Dir, "big"))
	require.NoError(t, err)
	require.LessOrEqual(t, big.Size(), int64(256<<20))

	got = e.sandboxed(t, "head -c 300M /dev/zero > /tmp/big")
	require.False(t, got.Success, got.Output)

	e.sandboxedOK(t, "id -u; grep -E '^(CapEff|NoNewPrivs|Seccomp):' /proc/self/status", "1000\nCapEff:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n")
	require.False(t, e.bash(t, "unshare -U true").Success, "no nested user namespaces")
}

func TestEmulatedShellLimits(t *testing.T) {
	e := newEmulated(t, map[string]string{"slow": "sleep 30", "late": "sleep 2; echo > late", "bigout": "head -c 20000000 /dev/zero", "drain": "cat > /dev/null"}, nil, nil)
	limitLogged := `level=WARN msg="emulated shell limit reached" conversation=web:c1 folder=` + regexp.QuoteMeta(e.shell.Dir) + ` cwd=\S+ script_bytes=\d+ `

	got := e.sss.Bash(t.Context(), &e.shell, bashParams{Command: "while :; do :; done", Workdir: e.shell.Dir, TimeoutMillisecond: 1000})
	require.Equal(t, "timeout", got.ErrorCode, "E01")
	require.Regexp(t, limitLogged+`exec=x-\S+ limit=timeout timeout=1s\n`, e.logs.String())

	got = e.bash(t, ":(){ :|:& };: ; sleep 3")
	require.Contains(t, got.Output, "process (64) limit reached", "E02")
	require.Regexp(t, limitLogged+`exec=x-\S+ limit=processes limit_processes=64 refused_forks=[1-9]\d*\n`, e.logs.String())

	got = e.bash(t, `python3 -c 'b = bytearray(512 << 20)'`)
	require.False(t, got.Success, "E03: %s", got.Output)
	require.Contains(t, got.Output, "memory (256 MiB)")
	require.Regexp(t, limitLogged+`exec=x-\S+ limit=memory limit_bytes=268435456 oom_kills=[1-9]\d*\n`, e.logs.String())

	got = e.bash(t, "yes")
	require.Contains(t, got.Output, "output stopped at 16 MiB", "E04")
	require.Regexp(t, limitLogged+`exec=x-\S+ limit=output limit_bytes=16777216\n`, e.logs.String())

	got = e.bash(t, "bigout | wc -c")
	require.Contains(t, got.Output, "output stopped at 16 MiB", "E04: a host command's output counts too")
	require.Regexp(t, limitLogged+`exec=x-\S+ command=bigout limit=host_command_output limit_bytes=16777216\n`, e.logs.String())

	e.sandboxedOK(t, "head -c 17000000 /dev/zero | drain > /dev/null 2>&1; echo $?", "126\n")
	require.Regexp(t, limitLogged+`exec=x-\S+ limit=host_command_input command=drain limit_bytes=16777216 received_bytes=\d{8}\n`, e.logs.String())

	require.False(t, e.bash(t, strings.Repeat(":", 8<<20+1)).Success)
	require.Regexp(t, `level=WARN msg="emulated shell limit reached" conversation=web:c1 folder=\S+ cwd=\S+ script_bytes=8388609 limit=script limit_bytes=8388608\n`, e.logs.String())

	start := time.Now()

	e.ok(t, "(sleep 2; echo late > late) & sleep 30 & echo started", "started\n")
	require.Less(t, time.Since(start), 5*time.Second, "E05: background jobs do not hold the command")

	time.Sleep(3 * time.Second)
	require.NoFileExists(t, filepath.Join(e.shell.Dir, "late"), "E05: background jobs die with the command")

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	start = time.Now()
	got = e.sss.Bash(ctx, &e.shell, bashParams{Command: "slow", Workdir: e.shell.Dir})
	require.False(t, got.Success)
	require.Less(t, time.Since(start), 10*time.Second)

	e.ok(t, "/extensions/late & sleep 1; kill -TERM $!; wait $!; echo $?; sleep 2; test -e late || echo cancelled", "143\ncancelled\n")

	agents := make([]*emulated, 6)
	for i := range agents {
		agents[i] = newEmulated(t, nil, nil, nil)
	}

	var wg sync.WaitGroup

	for i, e := range agents {
		wg.Go(func() {
			want := strings.Repeat("x", i+1)
			got := e.bash(t, "printf "+want+" > f; cat f")

			if got.Output != want {
				t.Errorf("concurrent agent %d read %q", i, got.Output)
			}
		})
	}

	wg.Wait()

	if raceEnabled {
		return
	}

	bounded := newEmulated(t, map[string]string{"nop": "true"}, nil, nil)
	got = bounded.sss.Bash(t.Context(), &bounded.shell, bashParams{Command: "for i in $(seq 140); do nop || { echo stopped at $i; break; }; done", Workdir: bounded.shell.Dir})
	require.Contains(t, got.Output, "stopped at 129")
	require.Equal(t, 1, strings.Count(bounded.logs.String(), "limit=host_command_calls"), "the call limit logs once per run, not once per refused call")
	require.Regexp(t, `exec=x-\S+ limit=host_command_calls command=nop limit_calls=128\n`, bounded.logs.String())
}
