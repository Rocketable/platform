//go:build amd64 || arm64

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const cgroupRoot = "/sys/fs/cgroup"

// delegateCgroup returns a cgroup the sandbox owns inside RocketClaw's own delegated cgroup (systemd
// Delegate=yes). cgroup v2 forbids processes in a cgroup whose children have controllers, so RocketClaw
// moves into a "supervisor" leaf and the sandbox gets the sibling "sandbox". Commands a crash left there are
// killed and removed (spec §5.4).
func delegateCgroup() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("cgroup: %w", err)
	}

	own, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "0::")
	if !ok {
		return "", errors.New("cgroup v2 is required")
	}

	dir := filepath.Join(cgroupRoot, own)
	if filepath.Base(dir) == "supervisor" {
		dir = filepath.Dir(dir) // a reload: RocketClaw already moved
	}

	for _, leaf := range []string{"supervisor", "sandbox"} {
		if err := os.Mkdir(filepath.Join(dir, leaf), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("cgroup %s is not delegated to RocketClaw (systemd Delegate=yes): %w", dir, err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "supervisor", "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		return "", fmt.Errorf("cgroup: %w", err)
	}

	parent := filepath.Join(dir, "sandbox")
	for _, d := range []string{dir, parent} {
		if err := enableControllers(d); err != nil {
			return "", err
		}
	}

	stale, _ := os.ReadDir(parent)
	for _, e := range stale {
		if e.IsDir() {
			child := filepath.Join(parent, e.Name())
			if err := errors.Join(killCgroup(child), unix.Rmdir(child)); err != nil {
				return "", err
			}
		}
	}

	return parent, nil
}

// enableControllers turns on cpu, memory and pids for dir's children, which must all be available to it.
func enableControllers(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("cgroup v2 is required: %w", err)
	}

	for _, c := range []string{"cpu", "memory", "pids"} {
		if !slices.Contains(strings.Fields(string(data)), c) {
			return fmt.Errorf("cgroup controller %s is not delegated to %s", c, dir)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0); err != nil {
		return fmt.Errorf("enable cgroup controllers in %s: %w", dir, err)
	}

	return nil
}

// execCgroup is one command's cgroup.
type execCgroup struct {
	path string
	fd   int // for CLONE_INTO_CGROUP
}

// newExecCgroup creates dir with the limits set before any process enters it.
func newExecCgroup(dir string) (*execCgroup, error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cgroup: %w", err)
	}

	cg := &execCgroup{path: dir, fd: -1}

	for file, value := range map[string]string{
		"memory.max": strconv.Itoa(memoryBytes),
		"pids.max":   strconv.Itoa(tasks),
		"cpu.max":    fmt.Sprintf("%d 100000", cpuMillis*100),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0); err != nil {
			_ = cg.remove()
			return nil, fmt.Errorf("cgroup %s: %w", file, err)
		}
	}

	// Swap would let the guest exceed memory.max; the file is missing only without swap accounting.
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = cg.remove()
		return nil, fmt.Errorf("cgroup memory.swap.max: %w", err)
	}

	fd, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = cg.remove()
		return nil, fmt.Errorf("cgroup: %w", err)
	}

	cg.fd = fd

	return cg, nil
}

// killCgroup kills every process in dir and waits until none is left.
func killCgroup(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil {
		return fmt.Errorf("cgroup.kill: %w", err)
	}

	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		if populated, err := cgroupKey(dir, "cgroup.events", "populated"); err != nil || populated == 0 {
			return err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("processes survived cgroup.kill in %s", dir)
		}
	}
}

// cgroupKey reads one "key value" line of a flat-keyed cgroup file.
func cgroupKey(dir, file, key string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0, fmt.Errorf("cgroup %s: %w", file, err)
	}

	for line := range strings.Lines(string(data)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok && k == key {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("cgroup %s %s: %w", file, key, err)
			}

			return n, nil
		}
	}

	return 0, fmt.Errorf("%s: no %s", file, key)
}

// hitResourceLimit reports whether the cgroup killed for memory or refused a fork.
func (cg *execCgroup) hitResourceLimit() bool {
	oom, _ := cgroupKey(cg.path, "memory.events", "oom_kill")
	pids, _ := cgroupKey(cg.path, "pids.events", "max")

	return oom > 0 || pids > 0
}

// remove closes the descriptor and removes the cgroup, which killCgroup emptied.
func (cg *execCgroup) remove() error {
	if cg.fd >= 0 {
		_ = unix.Close(cg.fd)
	}

	if err := unix.Rmdir(cg.path); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove cgroup: %w", err)
	}

	return nil
}

// cgroupRelative is dir as /proc/<pid>/cgroup reports it.
func cgroupRelative(dir string) (string, error) {
	rel, err := filepath.Rel(cgroupRoot, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("cgroup %s is not under %s", dir, cgroupRoot)
	}

	return "/" + rel, nil
}
