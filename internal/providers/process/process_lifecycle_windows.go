//go:build windows

package process

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"syscall"
)

const processQueryLimitedInformation = 0x1000
const windowsStillActive = 259

func nativeProcessAlive(pid int) (bool, error) {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER means that no process has this ID.
		if errors.Is(err, syscall.Errno(87)) {
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, err
	}
	return exitCode == windowsStillActive, nil
}

func stopNativeProcess(ctx context.Context, pid int) error {
	// The native runner launches a command script and then its listener and job
	// processes. Terminate the full descendant tree so cleanup cannot leave a
	// child workflow running after RunnerHub has stopped its worker.
	cmd := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	if err := cmd.Run(); err == nil {
		return nil
	}
	alive, checkErr := nativeProcessAlive(pid)
	if checkErr == nil && !alive {
		return nil
	}
	return errors.New("could not stop native runner process tree")
}
