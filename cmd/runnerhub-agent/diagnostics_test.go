package main

import (
	"strings"
	"testing"
)

func TestSanitizeSupportDiagnosticsRedactsSecrets(t *testing.T) {
	in := "Authorization: Bearer super-secret-token\npassword=hunter2\ngithub_pat_abcdefghijklmnopqrstuvwxyz\nstartup failed"
	got := sanitizeSupportDiagnostics(in)
	if strings.Contains(got, "super-secret-token") || strings.Contains(got, "hunter2") || strings.Contains(got, "github_pat_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret leaked from diagnostics: %q", got)
	}
	if !strings.Contains(got, "startup failed") {
		t.Fatalf("useful startup evidence missing: %q", got)
	}
}

func TestSanitizeSupportDiagnosticsStopsBeforeJobOutput(t *testing.T) {
	got := sanitizeSupportDiagnostics("runner registered\njob execution started\nsecret user output")
	if !strings.Contains(got, "runner registered") || !strings.Contains(got, "[job output omitted]") {
		t.Fatalf("unexpected diagnostics: %q", got)
	}
	if strings.Contains(got, "secret user output") {
		t.Fatalf("job output retained: %q", got)
	}
}

func TestSanitizeSupportDiagnosticsIsBounded(t *testing.T) {
	// Keep individual token-like runs short so this exercises the size bound,
	// rather than the long-opaque-token redactor.
	got := sanitizeSupportDiagnostics(strings.Repeat("diagnostic line x\n", maxSupportDiagnosticsBytes/8))
	if len(got) > maxSupportDiagnosticsBytes+32 {
		t.Fatalf("diagnostics are not bounded: %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "[truncated]\n") {
		t.Fatalf("truncation marker missing")
	}
}
