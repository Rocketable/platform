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

const (
	suite    = "resolute"
	packages = "bash,bzip2,ca-certificates,coreutils,diffutils,file,findutils,gawk,grep,gzip,jq,nodejs,procps,python3,python3-numpy,python3-scipy,sed,sqlite3,tar,wget,xz-utils"
)

//go:embed ubuntu-archive-keyring.gpg
var keyring []byte

func BuildImage(ctx context.Context, snapshot, arch, dst, cgroup string) (installed []string, release string, err error) {
	work := dst + ".work"
	if err := os.RemoveAll(work); err != nil {
		return nil, "", fmt.Errorf("clear %s: %w", work, err)
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", work, err)
	}
	defer func() { _ = os.RemoveAll(work) }()

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
		"--aptopt=Acquire::http::Pipeline-Depth 0"}

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

func clearBuild(tmp string) error {
	helper := exec.Command("mmdebstrap", "--unshare-helper", "find", tmp, "-mindepth", "2", "-delete")
	helper.Dir = "/"
	_ = helper.Run()

	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clear build folder: %w", err)
	}

	return nil
}

func pruneCache(archives string, installed []string) error {
	debs, err := filepath.Glob(filepath.Join(archives, "*.deb"))
	if err != nil {
		return fmt.Errorf("list package cache: %w", err)
	}

	for _, deb := range debs {
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
			w, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, "", fmt.Errorf("write %s: open: %w", name, err)
			}

			_, err = io.Copy(w, tr)
			if err := errors.Join(err, w.Close(), root.Chmod(name, mode)); err != nil {
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
		if err := errors.Join(err, w.Close()); err != nil {
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
