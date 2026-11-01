//go:build linux

package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// C: the guest's only network is curl, which follows webfetch's rules (R34, the shared table); no other
// program reaches any address, while the agent's host commands use the host's network.
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

			// The table checks the rule, as webfetch's test does; example.com hosts need not resolve here.
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

	// C10: no other program reaches the network, even an address curl may fetch.
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

	// A downloaded ELF, here this test binary, cannot dial out either.
	e.ok(t, "ln -s /extensions/.rocketclaw/rocketclaw /tmp/rocketclaw-forge && /tmp/rocketclaw-forge dial 1.1.1.1:443 | cut -c1-5", "dial:\n")

	// C12: the agent's host command uses the host's network, to an address the guest's curl may not fetch.
	e.ok(t, "hostfetch", "unlisted")
	require.Contains(t, e.bash(t, "curl -sS "+unlisted.URL+"/").Output, "not in allowedNetworkTargets")
}

// D: the guest sees its folders and the pinned rootfs, read-only but for its folder; no host file, socket or
// other agent's folder; and host-side writes for it stay inside its folder.
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

	for _, path := range []string{marker, filepath.Join(other.shell.Dir, "theirs"), os.Getenv("HOME") + "/.bashrc", "/etc/shadow", "/var/run/docker.sock", "/run/dbus/system_bus_socket", "/sys/kernel"} {
		got := e.bash(t, "cat "+path)
		require.False(t, got.Success, "D01, D04, D07: %s is visible: %s", path, got.Output)
	}

	e.ok(t, "cat "+filepath.Join(uploads, "report.txt"), "upload")
	require.False(t, e.bash(t, "echo x > "+filepath.Join(uploads, "new")).Success, "uploads are read-only")
	require.False(t, e.bash(t, "touch /usr/bin/evil || touch /evil || touch /etc/evil").Success, "D02: the rootfs is read-only")

	// D03: links the guest plants cannot move host-side work out of its folder.
	require.False(t, e.bash(t, "ln -s "+filepath.Dir(marker)+" out && cd out").Success, "a link out of the folder leads nowhere in the guest")

	for _, target := range []string{"out/host-marker", "../escape", marker, "/etc/passwd"} {
		got := e.bash(t, "curl -so "+target+" "+server.URL+"/")
		require.Equal(t, "23", got.ErrorCode, "curl -o %s wrote outside the folder: %s", target, got.Output)
	}

	e.ok(t, "curl -so inside "+server.URL+"/ && cat inside", "evil")

	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "host", string(data))
	require.NoFileExists(t, filepath.Join(filepath.Dir(e.shell.Dir), "escape"))

	// D05: one file cannot exceed the file limit, nor /tmp its size.
	got := e.bash(t, "head -c 300M /dev/zero > big")
	require.False(t, got.Success, got.Output)
	require.Contains(t, got.Output, "File size limit exceeded")

	got = e.bash(t, "head -c 300M /dev/zero > /tmp/big")
	require.False(t, got.Success, got.Output)

	// The guest runs as an unprivileged user with no capabilities, no new privileges, and seccomp on.
	e.ok(t, "id -u; grep -E '^(CapEff|NoNewPrivs|Seccomp):' /proc/self/status", "1000\nCapEff:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n")
	require.False(t, e.bash(t, "unshare -U true").Success, "no nested user namespaces")
}

// E: limits stop runaway commands and nothing survives a command.
func TestEmulatedShellLimits(t *testing.T) {
	e := newEmulated(t, map[string]string{"slow": "sleep 30"}, nil, nil)

	got := e.sss.Bash(t.Context(), &e.shell, bashParams{Command: "while :; do :; done", Workdir: e.shell.Dir, TimeoutMillisecond: 1000})
	require.Equal(t, "timeout", got.ErrorCode, "E01")

	got = e.bash(t, ":(){ :|:& };: ; sleep 3")
	require.Contains(t, got.Output, "process (64) limit reached", "E02")

	got = e.bash(t, `python3 -c 'b = bytearray(512 << 20)'`)
	require.False(t, got.Success, "E03: %s", got.Output)
	require.Contains(t, got.Output, "memory (256 MiB)")

	got = e.bash(t, "yes")
	require.Contains(t, got.Output, "output stopped at 16 MiB", "E04")

	start := time.Now()

	e.ok(t, "(sleep 2; echo late > late) & sleep 30 & echo started", "started\n")
	require.Less(t, time.Since(start), 5*time.Second, "E05: background jobs do not hold the command")

	time.Sleep(3 * time.Second)
	require.NoFileExists(t, filepath.Join(e.shell.Dir, "late"), "E05: background jobs die with the command")

	// E06: cancelling stops the guest and the host command.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	start = time.Now()
	got = e.sss.Bash(ctx, &e.shell, bashParams{Command: "slow", Workdir: e.shell.Dir})
	require.False(t, got.Success)
	require.Less(t, time.Since(start), 10*time.Second)

	// E09: agents run concurrently, independently. Each is set up first: preparing is the backend's once-per-
	// process step, not something commands race on.
	var wg sync.WaitGroup

	for i := range 6 {
		e := newEmulated(t, nil, nil, nil)

		wg.Go(func() {
			want := strings.Repeat("x", i+1)
			got := e.bash(t, "printf "+want+" > f; cat f")

			if got.Output != want {
				t.Errorf("concurrent agent %d read %q", i, got.Output)
			}
		})
	}

	wg.Wait()

	// E10: host commands per shell command are bounded.
	// Under -race each relay starts slowly, so this one gets more than the default two minutes.
	bounded := newEmulated(t, map[string]string{"nop": "true"}, nil, nil)
	got = bounded.sss.Bash(t.Context(), &bounded.shell, bashParams{Command: "for i in $(seq 140); do nop || { echo stopped at $i; break; }; done", Workdir: bounded.shell.Dir, TimeoutMillisecond: 600000})
	require.Contains(t, got.Output, "stopped at 129")
}
