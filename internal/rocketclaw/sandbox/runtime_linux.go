//go:build amd64 || arm64

package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Prepare readies the sandbox under dir: it unpacks rootfs, the zstd tar `go generate` embeds in RocketClaw,
// once per content, takes over RocketClaw's delegated cgroup, and runs a probe. On any failure the Runtime
// stays unavailable with the reason, so emulated shells refuse rather than run unsandboxed (spec §5.3).
func Prepare(ctx context.Context, rootfs []byte, dir string) *Runtime {
	rt, err := prepare(ctx, rootfs, dir)
	if err != nil {
		return &Runtime{err: err}
	}

	return rt
}

func prepare(ctx context.Context, rootfs []byte, dir string) (*Runtime, error) {
	if len(rootfs) == 0 {
		return nil, errors.New("this RocketClaw build has no sandbox rootfs (run go generate ./cmd/rocketclaw)")
	}

	sum := sha256.Sum256(rootfs)

	rt := &Runtime{rootfs: filepath.Join(dir, "rootfs-"+hex.EncodeToString(sum[:8])), state: filepath.Join(dir, "state")}

	if err := unpack(rootfs, dir, rt.rootfs); err != nil {
		return nil, fmt.Errorf("unpack rootfs: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find RocketClaw: %w", err)
	}

	if rt.self, err = filepath.EvalSymlinks(self); err != nil {
		return nil, fmt.Errorf("find RocketClaw: %w", err)
	}

	if rt.bwrap, err = exec.LookPath("bwrap"); err != nil {
		return nil, fmt.Errorf("bubblewrap is required: %w", err)
	}

	if rt.seccomp, err = seccompProgram(); err != nil {
		return nil, err
	}

	if rt.cgroup, err = delegateCgroup(); err != nil {
		return nil, err
	}

	if err := os.RemoveAll(rt.state); err != nil {
		return nil, fmt.Errorf("clear state: %w", err)
	}

	if err := os.Mkdir(rt.state, 0o700); err != nil {
		return nil, fmt.Errorf("create state: %w", err)
	}

	// A probe proves every isolation layer works here (E08); a refused namespace or seccomp filter fails now.
	res, err := rt.Run(ctx, &Command{Script: "test -r /proc/self/status", Cwd: rt.state, Dir: rt.state, Timeout: 30 * time.Second}, probeHost{})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("probe exited %d: %s", res.ExitCode, res.Output)
	}

	if err != nil {
		return nil, fmt.Errorf("sandbox probe: %w", err)
	}

	return rt, nil
}

// unpack extracts the rootfs tar into dst unless an earlier start did, and removes other versions.
func unpack(archive []byte, dir, dst string) error {
	old, _ := filepath.Glob(filepath.Join(dir, "rootfs-*"))
	for _, o := range old {
		if o != dst {
			if err := os.RemoveAll(o); err != nil {
				return fmt.Errorf("remove old rootfs: %w", err)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(dst, ".complete")); err == nil {
		return nil
	}

	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear %s: %w", dst, err)
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	root, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = root.Close() }()

	zr, err := zstd.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("zstd: %w", err)
	}
	defer zr.Close()

	// Directory modes are set last, so read-only ones do not block their own contents.
	type dirMode struct {
		name string
		mode fs.FileMode
	}

	var dirs []dirMode

	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}

		mode := fs.FileMode(h.Mode).Perm()

		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("mkdir %s: %w", name, err)
			}

			dirs = append(dirs, dirMode{name, mode})
		case tar.TypeReg:
			if err := writeFile(root, name, tr, mode); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		case tar.TypeSymlink:
			if err := root.Symlink(h.Linkname, name); err != nil {
				return fmt.Errorf("symlink %s: %w", name, err)
			}
		case tar.TypeLink:
			if err := root.Link(filepath.Clean(h.Linkname), name); err != nil {
				return fmt.Errorf("link %s: %w", name, err)
			}
		default:
			return fmt.Errorf("%s: unexpected tar entry type %q", name, h.Typeflag)
		}
	}

	for _, d := range slices.Backward(dirs) {
		if err := root.Chmod(d.name, d.mode|0o500); err != nil {
			return fmt.Errorf("chmod %s: %w", d.name, err)
		}
	}

	if err := root.WriteFile(".complete", nil, 0o444); err != nil {
		return fmt.Errorf("mark complete: %w", err)
	}

	return nil
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

	// Setuid and setgid never survive (spec §4.2); the build already dropped them.
	if err := root.Chmod(name, mode&^(fs.ModeSetuid|fs.ModeSetgid)); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	return nil
}
