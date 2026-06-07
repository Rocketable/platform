//go:build windows

package harnessbridge

import (
	"errors"
	"os"
)

func lockStateStoreFile(*os.File) error {
	return errors.New("rocketcode session db lock is unsupported on windows")
}

func unlockStateStoreFile(*os.File) error {
	return nil
}
