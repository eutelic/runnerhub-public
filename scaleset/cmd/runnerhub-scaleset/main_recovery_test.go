package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestListenerRecoveryStatusDistinguishesRecovered409(t *testing.T) {
	at := time.Date(2026, time.November, 6, 16, 0, 0, 0, time.UTC)
	retry := listenerRetryState{
		attempt:      2,
		firstFailure: at.Add(-28 * time.Second),
		lastFailure: "GitHub message session conflict (409)",
	}
	detail := listenerRecoveryStatus(retry, at)
	for _, want := range []string{
		"recovered from transient GitHub message session conflict (409)",
		"after 2 retries in 28s",
		"2026-11-06T16:00:00Z",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("recovery detail %q missing %q", detail, want)
		}
	}
	if got := listenerRecoveryStatus(retry, at); got != detail {
		t.Fatalf("recovered listener detail should be stable across reconciliation: %q != %q", got, detail)
	}
	retry.lastFailure = "upstream authorization denied (403)"
	if got := listenerRecoveryStatus(retry, at); got != "" {
		t.Fatalf("unrelated listener failure was classified as a 409 recovery: %q", got)
	}
	retry.lastFailure = "GitHub message session conflict (409)"
	retry.attempt = 0
	if got := listenerRecoveryStatus(retry, at); got != "" {
		t.Fatalf("zero-retry listener reported a recovered conflict: %q", got)
	}
	retry.attempt = 1
	retry.firstFailure = at.Add(time.Minute)
	if got := listenerRecoveryStatus(retry, at); !strings.Contains(got, "in 0s") {
		t.Fatalf("clock skew should not produce a negative recovery duration: %q", got)
	}
}

func TestBlockedSessionCloseStatusNeverPersistsUpstreamSecrets(t *testing.T) {
	raw := errors.New("409 Conflict Authorization: Bearer SECRET-SESSION-TOKEN; https://github.com/example/private?token=SECRET")
	detail := blockedSessionCloseDetail(raw)
	if !strings.Contains(detail, "replacement blocked") ||
		strings.Contains(detail, "SECRET") || strings.Contains(detail, "Bearer") ||
		strings.Contains(detail, "github.com") {
		t.Fatalf("unsafe blocked-session status: %q", detail)
	}
	if classified := listenerFailureSummary(raw); classified != "GitHub message session conflict (409)" {
		t.Fatalf("raw upstream error was not classified safely: %q", classified)
	}
}
