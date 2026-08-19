package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessHandlerMarksOnlyReadyBoundary(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "scaleset-ready")
	h := &readinessHandler{
		next: slog.NewTextHandler(io.Discard, nil),
		path: path,
	}
	logger := slog.New(h).With("test", true)

	logger.Info("listener starting")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("readiness file exists before ready boundary: %v", err)
	}

	logger.Info(scaleSetReadyMessage, "scaleSetID", 42)
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
}

func TestReadinessHandlerSupportsGroups(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ready")
	base := &readinessHandler{next: slog.NewTextHandler(io.Discard, nil), path: path}
	logger := slog.New(base).WithGroup("listener").With("component", "scaleset")
	logger.Info(scaleSetReadyMessage)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("grouped logger did not create readiness file: %v", err)
	}
}

func TestWriteReadinessFileFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing", "ready")
	if err := writeReadinessFile(path); err == nil {
		t.Fatal("writeReadinessFile unexpectedly succeeded without parent directory")
	}
}

func TestMainStillEmitsReadinessBoundary(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	want := []byte(`slog.Info("scale-set listener ready"`)
	// main.go contains source text, not an escaped Go string. Keep the fallback
	// below explicit so this test catches an intentional log-boundary change.
	if !bytes.Contains(source, []byte(`slog.Info("scale-set listener ready"`)) &&
		!bytes.Contains(source, []byte(`slog.Info("scale-set listener ready"`)) {
		t.Fatalf("main.go no longer emits readiness boundary %q; update the readiness health contract deliberately (wanted %q)", scaleSetReadyMessage, want)
	}
}

func TestReadinessHandlerKeepsInfoObservable(t *testing.T) {
	t.Parallel()

	next := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := &readinessHandler{next: next, path: filepath.Join(t.TempDir(), "ready")}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("readiness handler must keep Info records observable for the readiness boundary")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("readiness handler should preserve enabled downstream levels")
	}
}
