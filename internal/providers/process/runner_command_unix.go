//go:build !windows

package process

import (
	"context"
	"os/exec"
	"path/filepath"
)

func nativeRunnerCommand(ctx context.Context, runnerDir string) *exec.Cmd {
	return exec.CommandContext(ctx, filepath.Join(runnerDir, "run.sh"))
}
