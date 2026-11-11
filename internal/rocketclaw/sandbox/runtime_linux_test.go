//go:build amd64 || arm64

package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestMain lets this binary stand in for mmdebstrap: it writes a small Ubuntu-like tar where the real one
// would, with the things the build must strip, add or refuse.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) != "mmdebstrap" {
		os.Exit(m.Run())
	}

	// The build clears its folder through mmdebstrap's helper, which runs the rest as the build's uid.
	if os.Args[1] == "--unshare-helper" {
		path, err := exec.LookPath(os.Args[2])
		if err == nil {
			err = unix.Exec(path, os.Args[2:], os.Environ())
		}

		panic(err)
	}

	f, err := os.Create(os.Args[slices.Index(os.Args, suite)+1])
	if err != nil {
		panic(err)
	}

	type entry struct {
		h    tar.Header
		data string
	}

	tw := tar.NewWriter(f)
	entries := []entry{
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/", Mode: 0o555}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/bin/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/lib/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/local/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/local/bin/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./etc/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./var/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./var/lib/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeDir, Name: "./var/lib/dpkg/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeSymlink, Name: "./bin", Linkname: "usr/bin", Mode: 0o777}, ""},
		{tar.Header{Typeflag: tar.TypeChar, Name: "./dev/null", Mode: 0o666}, ""},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/bin/su", Mode: 0o4755, Uid: 7, Uname: "root"}, "#!/bin/true"},
		{tar.Header{Typeflag: tar.TypeLink, Name: "./usr/bin/sudo", Linkname: "./usr/bin/su"}, ""},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./etc/passwd", Mode: 0o644}, "root:x:0:0:root:/root:/bin/bash\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./etc/group", Mode: 0o644}, "root:x:0:\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/lib/os-release", Mode: 0o644}, "PRETTY_NAME=\"Ubuntu 26.04 LTS\"\nVERSION_ID=\"26.04\"\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./var/lib/dpkg/status", Mode: 0o644}, "Package: zlib1g\nStatus: install ok installed\nVersion: 1:1.3\n\nPackage: gone\nStatus: deinstall ok config-files\nVersion: 2\n\nPackage: bash\nStatus: install ok installed\nVersion: 5.3-2ubuntu1\n"},
	}

	mode := os.Getenv("ROOTFS_TEST_MODE")
	if name, ok := strings.CutPrefix(mode, "drop "); ok {
		entries = slices.DeleteFunc(entries, func(e entry) bool { return e.h.Name == name })
	}

	switch mode {
	case "fail":
		fmt.Fprint(os.Stderr, strings.Repeat("I: downloading\n", 500), "E: cannot reach the archive\nE: mmdebstrap failed to run\n")
		os.Exit(25)
	case "fail without E":
		fmt.Fprint(os.Stderr, "W: odd\nI: gave up\n")
		os.Exit(2)
	case "fail quietly":
		os.Exit(1)
	case "fail twice":
		// The archive is flaky for two tries; ROOTFS_TEST_TRIES counts them.
		tries, _ := os.ReadFile(os.Getenv("ROOTFS_TEST_TRIES"))
		if err := os.WriteFile(os.Getenv("ROOTFS_TEST_TRIES"), append(tries, 'x'), 0o644); err != nil {
			panic(err)
		}

		if len(tries) < 2 {
			fmt.Fprint(os.Stderr, "Err:1 https://snapshot.ubuntu.com resolute InRelease\n  503  Service Unavailable\nE: Failed to fetch InRelease  503  Service Unavailable\n")
			os.Exit(100)
		}
	case "hang":
		// Like mmdebstrap: a folder in TMPDIR, and a helper on its output that outlives it unless its cgroup goes.
		dir, err := os.MkdirTemp("", "mmdebstrap.")
		helper := exec.Command("sleep", "infinity")
		helper.Stdout = os.Stdout

		ready := fmt.Sprintf("%d %s", os.Getpid(), dir)
		if err = errors.Join(err, helper.Start(), os.WriteFile(os.Getenv("ROOTFS_TEST_READY"), []byte(ready), 0)); err != nil {
			panic(err)
		}

		time.Sleep(time.Hour)
	case "duplicate":
		entries = append(entries, entries[slices.IndexFunc(entries, func(e entry) bool { return e.h.Name == "./bin" })])
	case "dangling":
		entries = append(entries, entry{tar.Header{Typeflag: tar.TypeLink, Name: "./usr/bin/gone", Linkname: "./usr/bin/missing"}, ""})
	case "capability":
		entries = append(entries, entry{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/bin/ping", Mode: 0o755, Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "x"}}, "ping"})
	case "global":
		entries = append(entries, entry{tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "global", PAXRecords: map[string]string{"comment": "x"}}, ""})
	case "norelease":
		entries = slices.DeleteFunc(entries, func(e entry) bool { return e.h.Name == "./usr/lib/os-release" })
		entries = append(entries, entry{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/lib/os-release", Mode: 0o644}, "PRETTY_NAME=\"Ubuntu\"\n"})
	}

	for i := range entries {
		e := &entries[i]
		e.h.Size = int64(len(e.data))

		if err := tw.WriteHeader(&e.h); err != nil {
			panic(err)
		}

		if _, err := io.WriteString(tw, e.data); err != nil {
			panic(err)
		}
	}

	if err := tw.Close(); err != nil {
		panic(err)
	}

	if err := f.Close(); err != nil {
		panic(err)
	}

	os.Exit(0)
}

// fakeMmdebstrap puts this test binary on PATH as mmdebstrap.
func fakeMmdebstrap(t *testing.T) {
	t.Helper()

	self, err := os.Executable()
	require.NoError(t, err)

	bin := t.TempDir()
	require.NoError(t, os.Symlink(self, filepath.Join(bin, "mmdebstrap")))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The fake tool finishes its tar synchronously; only its exit sleep is unnecessary. The test process's
	// race runtime is already initialized, so its exit grace and race detection stay unchanged.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
}

// The build turns mmdebstrap's tar into the guest image: devices gone, setuid dropped, directories owner-
// writable, the guest user added and js-exec linked; it reports the installed packages and the release the
// image says it is. It refuses a file capability, an unknown entry or an image without a release, and fails
// with the reason on anything it cannot write or read back.
func TestBuildImage(t *testing.T) {
	fakeMmdebstrap(t)

	dst := filepath.Join(t.TempDir(), "rootfs")
	installed, release, err := BuildImage(t.Context(), Snapshot, "amd64", dst, "")
	require.NoError(t, err)
	require.Equal(t, []string{"bash=5.3-2ubuntu1", "zlib1g=1:1.3"}, installed, "installed packages, sorted; removed ones left out")
	require.Equal(t, "26.04", release)
	require.NoDirExists(t, dst+".work")

	require.NoFileExists(t, filepath.Join(dst, "dev", "null"), "device nodes are left out")
	require.FileExists(t, filepath.Join(dst, ".complete"))

	info, err := os.Lstat(filepath.Join(dst, "usr", "bin", "su"))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o755), info.Mode(), "setuid never survives")

	hard, err := os.Lstat(filepath.Join(dst, "usr", "bin", "sudo"))
	require.NoError(t, err)
	require.True(t, os.SameFile(info, hard), "hard links share the file")

	info, err = os.Stat(filepath.Join(dst, "usr"))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o755), info.Mode().Perm(), "directories keep owner write, so the image can be rebuilt or removed")

	for link, want := range map[string]string{"bin": "usr/bin", "usr/local/bin/js-exec": "/usr/bin/node"} {
		target, err := os.Readlink(filepath.Join(dst, link))
		require.NoError(t, err)
		require.Equal(t, want, target)
	}

	passwd, err := os.ReadFile(filepath.Join(dst, "etc", "passwd"))
	require.NoError(t, err)
	require.Equal(t, "root:x:0:0:root:/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n", string(passwd))

	for mode, want := range map[string]string{
		"capability":                 "usr/bin/ping has file capabilities",
		"global":                     "global: unexpected tar entry type",
		"norelease":                  "usr/lib/os-release has no VERSION_ID",
		"duplicate":                  "symlink bin:",
		"dangling":                   "link usr/bin/gone:",
		"drop ./etc/passwd":          "open etc/passwd:",
		"drop ./usr/local/bin/":      "link js-exec:",
		"drop ./var/lib/dpkg/":       "write var/lib/dpkg/status: open:",
		"drop ./var/lib/dpkg/status": "read dpkg status:",
		"drop ./usr/lib/os-release":  "read os-release:",
	} {
		t.Setenv("ROOTFS_TEST_MODE", mode)
		_, _, err = BuildImage(t.Context(), Snapshot, "amd64", dst, "")
		require.ErrorContains(t, err, want, mode)
	}

	// A failed mmdebstrap says why in its first error line, else its last line; its whole output stays with
	// the error for the server log.
	for mode, want := range map[string]string{
		"fail":           "mmdebstrap failed (exit status 25): E: cannot reach the archive",
		"fail without E": "mmdebstrap failed (exit status 2): I: gave up",
		"fail quietly":   "mmdebstrap failed (exit status 1)",
	} {
		t.Setenv("ROOTFS_TEST_MODE", mode)
		_, _, err = BuildImage(t.Context(), Snapshot, "amd64", dst, "")
		require.EqualError(t, err, want)
	}

	t.Setenv("ROOTFS_TEST_MODE", "fail")
	_, _, err = BuildImage(t.Context(), Snapshot, "amd64", dst, "")
	b, ok := errors.AsType[*BuildError](err)
	require.True(t, ok)
	require.Contains(t, string(b.Output), strings.Repeat("I: downloading\n", 500))

	t.Setenv("PATH", t.TempDir())
	_, _, err = BuildImage(t.Context(), Snapshot, "amd64", dst, "")
	require.ErrorIs(t, err, exec.ErrNotFound, "no mmdebstrap")
}

// A package cache carried over from an older lock keeps only the packages the new image installs.
func TestBuildImagePrunesPackageCache(t *testing.T) {
	fakeMmdebstrap(t)

	cache := t.TempDir()
	t.Setenv("ROCKETCLAW_SANDBOX_PACKAGE_CACHE", cache)

	archives := filepath.Join(cache, "archives")
	require.NoError(t, os.Mkdir(archives, 0o755))

	for _, name := range []string{"bash_5.3-2ubuntu1_amd64.deb", "zlib1g_1%3a1.3_amd64.deb", "bash_5.2-1_amd64.deb", "gone_2_all.deb", "lock"} {
		require.NoError(t, os.WriteFile(filepath.Join(archives, name), nil, 0o644))
	}

	_, _, err := BuildImage(t.Context(), Snapshot, "amd64", filepath.Join(t.TempDir(), "rootfs"), "")
	require.NoError(t, err)

	left, err := os.ReadDir(archives)
	require.NoError(t, err)

	names := make([]string, 0, len(left))
	for _, e := range left {
		names = append(names, e.Name())
	}

	require.Equal(t, []string{"bash_5.3-2ubuntu1_amd64.deb", "lock", "zlib1g_1%3a1.3_amd64.deb"}, names)
	require.DirExists(t, filepath.Join(cache, "lists"))
}

// A cancelled build, or one whose mmdebstrap alone is killed as the OOM killer does, leaves no process and no
// folder behind, and the next build clears what a crash left.
func TestBuildImageLeavesNothing(t *testing.T) {
	fakeMmdebstrap(t)
	t.Setenv("ROOTFS_TEST_MODE", "hang")

	ready := filepath.Join(t.TempDir(), "ready")
	require.NoError(t, unix.Mkfifo(ready, 0o600))
	t.Setenv("ROOTFS_TEST_READY", ready)

	own, err := os.ReadFile("/proc/self/cgroup")
	require.NoError(t, err)

	cgroup := filepath.Join(cgroupRoot, strings.TrimPrefix(strings.TrimSpace(string(own)), "0::"), "test-"+rand.Text())
	if err := os.Mkdir(cgroup, 0o755); err != nil {
		t.Skipf("WARNING: skipping: this test may not make a cgroup for the build: %v", err)
	}

	t.Cleanup(func() { _ = unix.Rmdir(cgroup) })

	dst := filepath.Join(t.TempDir(), "rootfs")

	var dir string

	for _, alone := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() { _, _, err := BuildImage(ctx, Snapshot, "amd64", dst, cgroup); done <- err }()

		started, err := os.ReadFile(ready)
		require.NoError(t, err)

		pid, folder, _ := strings.Cut(string(started), " ")
		dir = folder

		if n, _ := strconv.Atoi(pid); alone {
			require.NoError(t, unix.Kill(n, unix.SIGKILL))
		} else {
			cancel()
		}

		require.Error(t, <-done)
		cancel()
		require.NoDirExists(t, filepath.Dir(dir))
		require.NoDirExists(t, filepath.Join(cgroup, "build"), "no process of the build is left")
	}

	require.NoError(t, os.MkdirAll(dir, 0o755), "as if RocketClaw died mid-build")
	t.Setenv("ROOTFS_TEST_MODE", "")

	_, _, err = BuildImage(t.Context(), Snapshot, "amd64", dst, cgroup)
	require.NoError(t, err, "the next build clears the folder first")
}

// While mmdebstrap fails, as it does when Ubuntu's archive is flaky, the build tries again after growing pauses
// and says why and when, then carries on once a try works. A failure no retry fixes, no mmdebstrap at all,
// returns at once.
func TestBuildImageRetries(t *testing.T) {
	fakeMmdebstrap(t)
	t.Setenv("ROOTFS_TEST_MODE", "fail twice")
	t.Setenv("ROOTFS_TEST_TRIES", filepath.Join(t.TempDir(), "tries"))

	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer

		rt := &Runtime{p: &prepared{ready: make(chan struct{}), rootfs: filepath.Join(t.TempDir(), "rootfs")}}
		start := time.Now()

		type result struct {
			installed []string
			err       error
		}

		done := make(chan result, 1)

		go func() {
			installed, err := rt.buildImage(t.Context(), slog.New(slog.NewTextHandler(&logs, nil)))
			done <- result{installed, err}
		}()

		synctest.Wait()

		err := rt.Err()
		require.ErrorIs(t, err, ErrPreparing)
		require.ErrorContains(t, err, "the last try failed (mmdebstrap failed (exit status 100): E: Failed to fetch InRelease  503  Service Unavailable), and the next is at ")

		r := <-done
		require.NoError(t, r.err)
		require.Equal(t, []string{"bash=5.3-2ubuntu1", "zlib1g=1:1.3"}, r.installed)
		require.Equal(t, firstRetry+2*firstRetry, time.Since(start), "30 s, then twice that")
		require.Equal(t, 2, strings.Count(logs.String(), `msg="building the emulated shell's Ubuntu image failed; trying again"`))
	})

	t.Setenv("PATH", t.TempDir())

	_, err := (&Runtime{p: &prepared{rootfs: filepath.Join(t.TempDir(), "rootfs")}}).buildImage(t.Context(), slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, exec.ErrNotFound)
}

// A host folder under one of the image's folders, such as /var/lib/rocketclaw, gets that folder rebuilt around
// it from the image's entries, since bwrap cannot make a mount point inside a read-only bind. Everything else
// stays one read-only bind per top folder.
func TestImageBinds(t *testing.T) {
	image := t.TempDir()
	for _, d := range []string{"usr/bin", "var/lib/dpkg", "var/tmp", "var/cache", "etc", "srv", "home"} {
		require.NoError(t, os.MkdirAll(filepath.Join(image, d), 0o755))
	}

	require.NoError(t, os.Symlink("usr/bin", filepath.Join(image, "bin")))
	require.NoError(t, os.WriteFile(filepath.Join(image, ".complete"), nil, 0o444))

	rt := &Runtime{p: &prepared{rootfs: image}}
	args := rt.bwrapArgs(&Command{Dir: "/var/lib/rocketclaw/agent", ReadOnly: []string{"/srv/uploads"}}, t.TempDir())

	var mounts []string

	for i := 0; args[i] != "--proc"; i++ {
		if args[i] == "--ro-bind" || args[i] == "--symlink" {
			mounts = append(mounts, args[i+1]+" "+args[i+2])
			i += 2
		}
	}

	require.ElementsMatch(t, []string{
		"usr/bin /bin",
		image + "/etc /etc",
		image + "/usr /usr",
		image + "/var/cache /var/cache",
		image + "/var/lib/dpkg /var/lib/dpkg",
		image + "/var/tmp /var/tmp",
	}, mounts)
}

// Prepare fails closed, without waiting commands forever: without bubblewrap the Runtime refuses with what to
// do about it, which the server log records too.
func TestPrepareFailsClosed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var logs bytes.Buffer

	rt := Bubblewrap{}.Prepare(t.Context(), t.TempDir(), slog.New(slog.NewTextHandler(&logs, nil)))
	_, err := rt.Run(t.Context(), &Command{Script: "true"}, probeHost{})
	require.EqualError(t, err, "this machine doesn't have bubblewrap, which the sandbox is built on; install the bubblewrap package and restart RocketClaw")
	require.Equal(t, err, rt.Err())
	require.Contains(t, logs.String(), `level=ERROR msg="emulated shell unavailable" error="this machine doesn't have bubblewrap`)

	var zero Runtime

	_, err = zero.Run(t.Context(), &Command{Script: "true"}, probeHost{})
	require.ErrorIs(t, err, errNotReady)
}

// Outside a sandbox the guest entry points refuse: this is not the relay, there is no broker, and init checks
// its arguments before touching limits.
func TestGuestOutsideSandbox(t *testing.T) {
	_, ok := Guest([]string{"rocketclaw"})
	require.False(t, ok)

	require.Equal(t, 126, relay("deploy", nil))
	require.ErrorContains(t, guestInit(nil), "usage: rocketclaw init")
	require.ErrorContains(t, guestInit([]string{"x", "1024", "5", "--", "/bin/true"}), "init:")

	// When Bash cannot start, init says so after its ready byte, so the host knows no guest code ran. The
	// limits are this process's own, so it keeps working.
	var fsize, nofile unix.Rlimit
	require.NoError(t, unix.Getrlimit(unix.RLIMIT_FSIZE, &fsize))
	require.NoError(t, unix.Getrlimit(unix.RLIMIT_NOFILE, &nofile))

	r, w, err := os.Pipe()
	require.NoError(t, err)

	defer unix.Umask(unix.Umask(0o077))

	err = guestInit([]string{strconv.FormatUint(min(fsize.Max, 1<<62), 10), strconv.FormatUint(nofile.Max, 10), strconv.Itoa(int(w.Fd())), "--", "/nonexistent/bash"})
	require.ErrorContains(t, err, "exec /nonexistent/bash: no such file or directory")
	require.NoError(t, w.Close())

	ready, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "1E", string(ready))
}
