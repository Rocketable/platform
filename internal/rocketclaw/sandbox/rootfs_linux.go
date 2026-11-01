//go:build amd64 || arm64

package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
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
func BuildImage(ctx context.Context, snapshot, arch, dst string) (installed []string, release string, err error) {
	work := dst + ".work"
	if err := os.RemoveAll(work); err != nil {
		return nil, "", fmt.Errorf("clear %s: %w", work, err)
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", work, err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// mmdebstrap builds as root of a user namespace, a subordinate uid that cannot reach into a private home
	// folder, where work may be; apt reads the keyring as that uid, so it goes in /tmp, open to every uid, with
	// mmdebstrap's own files.
	key, err := os.CreateTemp("/tmp", "rocketclaw-keyring-*.gpg")
	if err != nil {
		return nil, "", fmt.Errorf("write keyring: %w", err)
	}
	defer func() { _ = os.Remove(key.Name()) }()

	_, err = key.Write(keyring)
	if err = errors.Join(err, key.Chmod(0o644), key.Close()); err != nil {
		return nil, "", fmt.Errorf("write keyring: %w", err)
	}

	raw := filepath.Join(work, "rootfs.tar")
	epoch, _ := time.Parse("20060102T150405Z", snapshot)
	mirror := "https://snapshot.ubuntu.com/ubuntu/" + snapshot

	var output bytes.Buffer

	cmd := exec.CommandContext(ctx, "mmdebstrap", "--mode=unshare", "--variant=essential", "--arch="+arch, "--format=tar",
		"--keyring="+key.Name(), "--aptopt=Acquire::Check-Valid-Until false", "--aptopt=Acquire::Retries 5",
		"--include="+packages, suite, raw,
		fmt.Sprintf("deb %s %s main universe", mirror, suite),
		fmt.Sprintf("deb %s %s-updates main universe", mirror, suite),
		fmt.Sprintf("deb %s %s-security main universe", mirror, suite))
	cmd.Env = append(os.Environ(), fmt.Sprintf("SOURCE_DATE_EPOCH=%d", epoch.Unix()), "TMPDIR=/tmp")
	cmd.Stdout, cmd.Stderr = &output, &output

	if err := cmd.Run(); err != nil {
		return nil, "", &BuildError{Err: err, Output: output.Bytes()}
	}

	return extract(raw, dst)
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
