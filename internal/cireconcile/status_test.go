package cireconcile

import (
	"errors"
	"testing"
	"time"
)

func TestStatusTracksSuccessAndSafeFailure(t *testing.T) {
	lookback := 30 * 24 * time.Hour
	status := NewStatus(lookback)
	started := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	status.begin(started, lookback)
	status.finish(started.Add(time.Second), nil)

	got := status.Snapshot()
	if !got.LastAttemptAt.Equal(started) || !got.LastSuccessAt.Equal(started.Add(time.Second)) {
		t.Fatalf("status timestamps=%+v", got)
	}
	if got.Lookback != lookback || got.LastError != "" {
		t.Fatalf("status=%+v", got)
	}

	status.begin(started.Add(time.Minute), lookback)
	status.finish(started.Add(time.Minute+time.Second), errors.New("secret-ish backend detail"))
	got = status.Snapshot()
	if got.LastError != "CI metadata reconciliation failed; see audit log" {
		t.Fatalf("unsafe analyzer error=%q", got.LastError)
	}
	if !got.LastSuccessAt.Equal(started.Add(time.Second)) {
		t.Fatalf("failure must preserve last success: %+v", got)
	}
}

func TestNewStatusUsesDefaultAnalyticsLookback(t *testing.T) {
	status := NewStatus(0)
	if got := status.Snapshot().Lookback; got != DefaultAnalyticsLookback {
		t.Fatalf("lookback=%v want %v", got, DefaultAnalyticsLookback)
	}
}
