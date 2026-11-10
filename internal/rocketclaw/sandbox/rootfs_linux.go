//go:build amd64 || arm64

package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The guest is Ubuntu 26.04 LTS from one archive snapshot (Snapshot, in snapshot.gen.go), so every package
// version is fixed; packages.<arch>.lock beside this file records them all, and a build whose packages differ
// fails. `go generate ./internal/rocketclaw/sandbox` moves it all to the newest snapshot (rootfs/main.go);
// commit what it changes, and each host builds the new image at its next start. A new Ubuntu release is a
// change of suite, then the same command.
const (
	suite = "resolute"
	// Installed on top of Ubuntu's essential set (spec §4.2). Adding one needs a security review, then go
	// generate.
	packages = "bash,bzip2,ca-certificates,coreutils,diffutils,file,findutils,gawk,grep,gzip,jq,nodejs,procps,python3,sed,sqlite3,tar,wget,xz-utils"
)

// The archive signing keys, from ubuntu-keyring 2023.11.28.1 in the snapshot (sha256
// 36de43b15853ccae0028e9a767613770c704833f82586f28eb262f0311adb8a8); trusted as committed.
//
//go:embed ubuntu-archive-keyring.gpg
var keyring []byte

// BuildImage builds the guest image for arch from the archive snapshot (a moment like Snapshot) into dst:
// mmdebstrap (Linux, unprivileged user namespaces, and qemu-user emulation for the other architecture)
// writes a tar, and extract writes it out without device nodes, setuid or setgid bits, or file capabilities,
// plus the guest user and js-exec. It returns the installed packages as name=version lines and the Ubuntu
// release. Prepare builds Snapshot's at the first start, and the update tool in rootfs/ the newest.
// mmdebstrap runs in a cgroup, build, under cgroup ("" for the caller's), killed when the build ends.
func BuildImage(ctx context.Context, snapshot, arch, dst, cgroup string) (installed []string, release string, err error) {
	work := dst + ".work"
	if err := os.RemoveAll(work); err != nil {
		return nil, "", fmt.Errorf("clear %s: %w", work, err)
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", work, err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// mmdebstrap builds as root of a user namespace, a subordinate uid that cannot reach into a private home
	// folder, where work may be, so it builds in /tmp, in one folder per image folder that each build clears.
	sum := sha256.Sum256([]byte(filepath.Dir(dst)))
	tmp := "/tmp/rocketclaw-build-" + hex.EncodeToString(sum[:8])

	if err := clearBuild(tmp); err != nil {
		return nil, "", err
	}

	if err := os.Mkdir(tmp, 0o755); err != nil {
		return nil, "", fmt.Errorf("create build folder: %w", err)
	}
	defer func() { _ = clearBuild(tmp) }()

	if err := os.Chmod(tmp, 0o755); err != nil {
		return nil, "", fmt.Errorf("create build folder: %w", err)
	}

	key := filepath.Join(tmp, "keyring.gpg")
	if err := errors.Join(os.WriteFile(key, keyring, 0o644), os.Chmod(key, 0o644)); err != nil {
		return nil, "", fmt.Errorf("write keyring: %w", err)
	}

	raw := filepath.Join(work, "rootfs.tar")
	epoch, _ := time.Parse("20060102T150405Z", snapshot)
	mirror := "https://snapshot.ubuntu.com/ubuntu/" + snapshot

	// ROCKETCLAW_SANDBOX_PACKAGE_MIRROR, when set, is an Ubuntu mirror tried first for the packages, as CI
	// sets the one beside its runners; the snapshot serves the indexes, and whatever packages the mirror lacks.
	// apt checks each package against the snapshot's signed indexes, so the versions stay pinned. The list's path
	// stays the same from build to build, as apt names a package cache's indexes after it.
	source := mirror

	if fast := os.Getenv("ROCKETCLAW_SANDBOX_PACKAGE_MIRROR"); fast != "" {
		list := filepath.Join(tmp, "mirrors.list")
		content := fmt.Sprintf("%s\tpriority:1 type:deb\n%s/\tpriority:2\n", fast, mirror)

		if err := errors.Join(os.WriteFile(list, []byte(content), 0o644), os.Chmod(list, 0o644)); err != nil {
			return nil, "", fmt.Errorf("write mirror list: %w", err)
		}

		source = "mirror+file:" + list
	}

	args := []string{"--mode=unshare", "--variant=essential", "--arch=" + arch, "--format=tar",
		"--keyring=" + key, "--aptopt=Acquire::Check-Valid-Until false", "--aptopt=Acquire::Retries 5",
		// Pipelined requests crawl through some networks' proxies, as on GitHub's runners, whose own apt has
		// pipelining off.
		"--aptopt=Acquire::http::Pipeline-Depth 0"}

	// ROCKETCLAW_SANDBOX_PACKAGE_CACHE, when set, is a folder that keeps the downloaded packages and indexes
	// between builds, as CI sets it, so a build fetches only the archive's signed InRelease files, which apt
	// checks the kept files against. mmdebstrap's cleanup leaves them out of the image.
	cache := os.Getenv("ROCKETCLAW_SANDBOX_PACKAGE_CACHE")
	if cache != "" {
		if cache, err = filepath.Abs(cache); err == nil {
			err = errors.Join(os.MkdirAll(filepath.Join(cache, "archives"), 0o755), os.MkdirAll(filepath.Join(cache, "lists"), 0o755))
		}

		if err != nil {
			return nil, "", fmt.Errorf("create package cache: %w", err)
		}

		args = append(args, "--skip=essential/unlink",
			`--setup-hook=mkdir -p "$1"/var/cache/apt/archives/ "$1"/var/lib/apt/lists/`,
			"--setup-hook=sync-in '"+filepath.Join(cache, "archives")+"' /var/cache/apt/archives/",
			"--setup-hook=sync-in '"+filepath.Join(cache, "lists")+"' /var/lib/apt/lists/",
			"--customize-hook=sync-out /var/cache/apt/archives '"+filepath.Join(cache, "archives")+"'",
			"--customize-hook=sync-out /var/lib/apt/lists '"+filepath.Join(cache, "lists")+"'")
	}

	// On a pipe, Run would wait for mmdebstrap's helpers, which outlive it when the OOM killer takes it alone.
	output, err := os.Create(filepath.Join(tmp, "output"))
	if err != nil {
		return nil, "", fmt.Errorf("create build output: %w", err)
	}
	defer func() { _ = output.Close() }()

	cmd := exec.CommandContext(ctx, "mmdebstrap", append(args, "--include="+packages, suite, raw,
		fmt.Sprintf("deb %s %s main universe", source, suite),
		fmt.Sprintf("deb %s %s-updates main universe", source, suite),
		fmt.Sprintf("deb %s %s-security main universe", source, suite))...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("SOURCE_DATE_EPOCH=%d", epoch.Unix()), "TMPDIR="+tmp)
	cmd.Stdout, cmd.Stderr = output, output

	build := filepath.Join(cgroup, "build")
	if cgroup != "" {
		if err := os.Mkdir(build, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, "", fmt.Errorf("cgroup: %w", err)
		}

		fd, err := unix.Open(build, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, "", fmt.Errorf("cgroup: %w", err)
		}
		defer func() { _ = unix.Close(fd) }()

		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
		cmd.Cancel = func() error { return killCgroup(build) }
	}

	errRun := cmd.Run()

	if cgroup != "" {
		if err := errors.Join(killCgroup(build), unix.Rmdir(build)); err != nil {
			return nil, "", err
		}
	}

	if errRun != nil {
		out, _ := os.ReadFile(output.Name())
		return nil, "", &BuildError{Err: errRun, Output: out}
	}

	if installed, release, err = extract(raw, dst); err == nil && cache != "" {
		err = pruneCache(filepath.Join(cache, "archives"), installed)
	}

	return installed, release, err
}

// clearBuild removes the build folder tmp; only mmdebstrap's helper can remove what its subordinate uid left.
func clearBuild(tmp string) error {
	helper := exec.Command("mmdebstrap", "--unshare-helper", "find", tmp, "-mindepth", "2", "-delete")
	helper.Dir = "/"
	_ = helper.Run()

	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clear build folder: %w", err)
	}

	return nil
}

// pruneCache deletes the cached packages the image no longer installs, so a cache carried over from an
// older lock keeps only what the next build needs.
func pruneCache(archives string, installed []string) error {
	debs, err := filepath.Glob(filepath.Join(archives, "*.deb"))
	if err != nil {
		return fmt.Errorf("list package cache: %w", err)
	}

	for _, deb := range debs {
		// apt names a package name_version_arch.deb, with the epoch's colon escaped.
		name, rest, _ := strings.Cut(strings.TrimSuffix(filepath.Base(deb), ".deb"), "_")
		version, _, _ := strings.Cut(rest, "_")

		if slices.Contains(installed, name+"="+strings.ReplaceAll(version, "%3a", ":")) {
			continue
		}

		if err := os.Remove(deb); err != nil {
			return fmt.Errorf("prune package cache: %w", err)
		}
	}

	return nil
}

// BuildError is a failed mmdebstrap. Its message is the first error line of the output, which is long and
// goes to the server log whole.
type BuildError struct {
	Err    error
	Output []byte
}

func (e *BuildError) Error() string {
	lines := strings.Split(strings.TrimSpace(string(e.Output)), "\n")

	reason := lines[len(lines)-1]
	if i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "E: ") }); i >= 0 {
		reason = lines[i]
	}

	if reason == "" {
		return fmt.Sprintf("mmdebstrap failed (%v)", e.Err)
	}

	return fmt.Sprintf("mmdebstrap failed (%v): %s", e.Err, reason)
}

func (e *BuildError) Unwrap() error { return e.Err }

// extract writes the mmdebstrap tar at raw into dst. Directories keep owner write, so the image can be
// rebuilt or removed; the sandbox mounts it read-only anyway. .complete marks the image done.
func extract(raw, dst string) (installed []string, release string, err error) {
	if err := os.RemoveAll(dst); err != nil {
		return nil, "", fmt.Errorf("clear %s: %w", dst, err)
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", dst, err)
	}

	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = root.Close() }()

	in, err := os.Open(raw)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", raw, err)
	}
	defer func() { _ = in.Close() }()

	tr := tar.NewReader(in)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", raw, err)
		}

		name := filepath.Clean(h.Name)
		if name == "." || h.Typeflag == tar.TypeChar || h.Typeflag == tar.TypeBlock || h.Typeflag == tar.TypeFifo || strings.HasPrefix(name, "dev/") {
			continue
		}

		if _, ok := h.PAXRecords["SCHILY.xattr.security.capability"]; ok {
			return nil, "", fmt.Errorf("%s has file capabilities (spec §4.2)", name)
		}

		// Perm drops setuid and setgid (spec §4.2).
		mode := fs.FileMode(h.Mode).Perm()

		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, "", fmt.Errorf("mkdir %s: %w", name, err)
			}

			if err := root.Chmod(name, mode|0o700); err != nil {
				return nil, "", fmt.Errorf("chmod %s: %w", name, err)
			}
		case tar.TypeReg:
			if err := writeFile(root, name, tr, mode); err != nil {
				return nil, "", fmt.Errorf("write %s: %w", name, err)
			}
		case tar.TypeSymlink:
			if err := root.Symlink(h.Linkname, name); err != nil {
				return nil, "", fmt.Errorf("symlink %s: %w", name, err)
			}
		case tar.TypeLink:
			if err := root.Link(filepath.Clean(h.Linkname), name); err != nil {
				return nil, "", fmt.Errorf("link %s: %w", name, err)
			}
		default:
			return nil, "", fmt.Errorf("%s: unexpected tar entry type %q", name, h.Typeflag)
		}
	}

	if installed, release, err = guestFiles(root); err != nil {
		return nil, "", err
	}

	if err := root.WriteFile(".complete", nil, 0o444); err != nil {
		return nil, "", fmt.Errorf("mark complete: %w", err)
	}

	return installed, release, nil
}

// guestFiles adds the guest user and js-exec to the extracted image, and reads from it what the lock and
// SBOM need: the installed packages and the Ubuntu release.
func guestFiles(root *os.Root) (installed []string, release string, err error) {
	for _, f := range []struct{ name, line string }{
		{"etc/passwd", "user:x:1000:1000::/home/user:/bin/bash\n"},
		{"etc/group", "user:x:1000:\n"},
	} {
		w, err := root.OpenFile(f.name, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, "", fmt.Errorf("open %s: %w", f.name, err)
		}

		_, err = w.WriteString(f.line)
		if errClose := w.Close(); err == nil {
			err = errClose
		}

		if err != nil {
			return nil, "", fmt.Errorf("add the guest user to %s: %w", f.name, err)
		}
	}

	if err := root.Symlink("/usr/bin/node", "usr/local/bin/js-exec"); err != nil {
		return nil, "", fmt.Errorf("link js-exec: %w", err)
	}

	status, err := root.ReadFile("var/lib/dpkg/status")
	if err != nil {
		return nil, "", fmt.Errorf("read dpkg status: %w", err)
	}

	osRelease, err := root.ReadFile("usr/lib/os-release")
	if err != nil {
		return nil, "", fmt.Errorf("read os-release: %w", err)
	}

	_, rest, _ := strings.Cut(string(osRelease), "VERSION_ID=\"")
	release, _, _ = strings.Cut(rest, "\"")

	if release == "" {
		return nil, "", errors.New("usr/lib/os-release has no VERSION_ID")
	}

	return dpkgInstalled(status), release, nil
}

func writeFile(root *os.Root, name string, r io.Reader, mode fs.FileMode) error {
	w, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	_, err = io.Copy(w, r)
	if errClose := w.Close(); err == nil {
		err = errClose
	}

	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := root.Chmod(name, mode); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	return nil
}

// dpkgInstalled lists the installed packages in dpkg's status file as sorted name=version lines.
func dpkgInstalled(status []byte) []string {
	var out []string

	var name, version, state string

	flush := func() {
		if state == "install ok installed" {
			out = append(out, name+"="+version)
		}

		name, version, state = "", "", ""
	}

	s := bufio.NewScanner(bytes.NewReader(status))
	s.Buffer(nil, 1<<20)

	for s.Scan() {
		k, v, _ := strings.Cut(s.Text(), ": ")

		switch k {
		case "":
			flush()
		case "Package":
			name = v
		case "Version":
			version = v
		case "Status":
			state = v
		}
	}

	flush()
	slices.Sort(out)

	return out
}
