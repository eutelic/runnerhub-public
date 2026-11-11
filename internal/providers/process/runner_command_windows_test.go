//go:build windows

package process

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNativeRunnerCommandUsesCmdForBatchScript(t *testing.T) {
	runnerDir := filepath.Join(`C:\Runner Hub`, "worker")
	cmd := nativeRunnerCommand(context.Background(), runnerDir)
	want := `cmd.exe /d /s /c ""` + filepath.Join(runnerDir, "run.cmd") + `""`
	if filepath.Base(cmd.Path) != "cmd.exe" {
		t.Fatalf("command path=%q, want cmd.exe", cmd.Path)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("command line=%q, want %q", cmd.SysProcAttr.CmdLine, want)
	}
}
