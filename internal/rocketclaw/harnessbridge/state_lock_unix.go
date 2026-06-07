//go:build !windows

package harnessbridge

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockStateStoreFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrStateStoreLocked
	}

	if err != nil {
		return fmt.Errorf("lock rocketcode session db: %w", err)
	}

	return nil
}

func unlockStateStoreFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("unlock rocketcode session db: %w", err)
	}

	return nil
}
