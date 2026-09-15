package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetReadinessWritesAndClearsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scaleset-ready")
	t.Setenv("RUNNERHUB_READY_FILE", path)

	if err := setReadiness(true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read readiness file: %v", err)
	}
	if string(got) != "ready\n" {
		t.Fatalf("readiness file = %q, want %q", got, "ready\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("readiness mode = %o, want 600", info.Mode().Perm())
	}

	if err := setReadiness(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("readiness marker still exists after clearing: %v", err)
	}
}

func TestSetReadinessWithoutConfiguredPathIsNoop(t *testing.T) {
	t.Setenv("RUNNERHUB_READY_FILE", "")
	if err := setReadiness(true); err != nil {
		t.Fatal(err)
	}
	if err := setReadiness(false); err != nil {
		t.Fatal(err)
	}
}

func TestWriteReadinessFileFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "ready")
	if err := writeReadinessFile(path); err == nil {
		t.Fatal("writeReadinessFile unexpectedly succeeded without parent directory")
	}
}

func TestClearReadinessFileMissingIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if err := clearReadinessFile(path); err != nil {
		t.Fatalf("clear missing readiness file: %v", err)
	}
}
