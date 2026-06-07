package harnessbridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrStateStoreLocked reports that another RocketClaw process owns the state store.
var ErrStateStoreLocked = errors.New("rocketclaw state store is locked")

// StateStoreLock holds advisory ownership of the RocketClaw state store.
type StateStoreLock struct {
	file *os.File
}

// AcquireStateStoreLock acquires non-blocking advisory ownership of the state store.
func AcquireStateStoreLock(workspace, workDir string) (*StateStoreLock, error) {
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	path := filepath.ToSlash(filepath.Join(workDir, "state.sqlite3.lock"))
	if _, err := rootPathExistsNoSymlink(root, path, "rocketcode session db lock"); err != nil {
		return nil, err
	}

	file, err := root.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open rocketcode session db lock: %w", err)
	}

	if err := lockStateStoreFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}

	return &StateStoreLock{file: file}, nil
}

// Close releases the state-store lock.
func (l *StateStoreLock) Close() error {
	errUnlock := unlockStateStoreFile(l.file)
	errClose := l.file.Close()

	return errors.Join(errUnlock, errClose)
}
