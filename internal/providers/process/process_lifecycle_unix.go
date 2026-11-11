//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"syscall"
)

func nativeProcessAlive(pid int) (bool, error) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func stopNativeProcess(_ context.Context, pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(os.Interrupt)
}
