package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const scaleSetReadyMessage = "scale-set listener ready"

func init() {
	path := strings.TrimSpace(os.Getenv("RUNNERHUB_READY_FILE"))
	if path == "" {
		return
	}
	slog.SetDefault(slog.New(&readinessHandler{next: slog.Default().Handler(), path: path}))
}

type readinessHandler struct {
	next slog.Handler
	path string
}

func (h *readinessHandler) Enabled(ctx context.Context, level slog.Level) bool {
	// The readiness boundary is currently emitted at Info. Keep Info records
	// observable by this wrapper even if a future downstream logger suppresses
	// them; Handle still honors downstream filtering for normal log output.
	return level == slog.LevelInfo || h.next.Enabled(ctx, level)
}

func (h *readinessHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == scaleSetReadyMessage {
		if err := writeReadinessFile(h.path); err != nil {
			// Do not mask the original log record or alter listener process
			// supervision. The missing marker intentionally leaves the container
			// unhealthy, while this diagnostic explains why.
			fmt.Fprintf(os.Stderr, "RunnerHub scale-set readiness marker: %v\n", err)
		}
	}
	if !h.next.Enabled(ctx, record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record)
}

func (h *readinessHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &readinessHandler{next: h.next.WithAttrs(attrs), path: h.path}
}

func (h *readinessHandler) WithGroup(name string) slog.Handler {
	return &readinessHandler{next: h.next.WithGroup(name), path: h.path}
}

func writeReadinessFile(path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("ready\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}
