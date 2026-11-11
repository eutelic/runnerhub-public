//go:build windows

package process

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"
)

func nativeRunnerCommand(ctx context.Context, runnerDir string) *exec.Cmd {
	runnerScript := filepath.Join(runnerDir, "run.cmd")
	cmd := exec.CommandContext(ctx, "cmd.exe")
	// Windows CreateProcess cannot execute a batch file directly. /d disables
	// command-processor AutoRun hooks, while /s /c preserves a quoted script
	// path when the service state directory contains spaces.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `cmd.exe /d /s /c ""` + runnerScript + `""`,
	}
	return cmd
}
