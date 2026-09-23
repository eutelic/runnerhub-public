package ciaudit

import (
	"testing"
	"time"
)

func TestEquivalentValidationFindingReusesContentAddressedCertificate(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	identity := ValidationIdentity{
		DefinitionHash: "definition",
		InputHash:      "inputs",
		ContextHash:    "context",
		Freshness:      FreshnessContentAddressed,
	}
	key, err := identity.Key()
	if err != nil {
		t.Fatal(err)
	}
	certificate := ValidationCertificate{
		Key:            key,
		Result:         "success",
		CompletedAt:    base.Add(-time.Minute),
		DefinitionHash: identity.DefinitionHash,
		InputHash:      identity.InputHash,
		ContextHash:    identity.ContextHash,
		Freshness:      identity.Freshness,
	}
	job := JobExecution{ID: "job-2", StartedAt: base, CompletedAt: base.Add(5 * time.Minute)}

	finding, ok := EquivalentValidationFinding(job, identity, certificate, 0)
	if !ok {
		t.Fatal("expected equivalent validation finding")
	}
	if finding.Class != EvidenceInputEquivalent || finding.DetectorID != "equivalent-validation" {
		t.Fatalf("finding=%+v", finding)
	}
	if finding.Interval.Start != job.StartedAt || finding.Interval.End != job.CompletedAt {
		t.Fatalf("interval=%+v", finding.Interval)
	}
}

func TestEquivalentValidationFindingFailsClosed(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	identity := ValidationIdentity{
		DefinitionHash: "definition",
		InputHash:      "inputs",
		ContextHash:    "context",
		Freshness:      FreshnessExternal,
	}
	key, err := identity.Key()
	if err != nil {
		t.Fatal(err)
	}
	certificate := ValidationCertificate{
		Key: key, Result: "success", CompletedAt: base.Add(-2 * time.Hour),
		DefinitionHash: identity.DefinitionHash, InputHash: identity.InputHash,
		ContextHash: identity.ContextHash, Freshness: identity.Freshness,
	}
	job := JobExecution{ID: "job", StartedAt: base, CompletedAt: base.Add(time.Minute)}

	if _, ok := EquivalentValidationFinding(job, identity, certificate, time.Hour); ok {
		t.Fatal("expired external certificate must not be reused")
	}

	live := identity
	live.Freshness = FreshnessLive
	liveKey, err := live.Key()
	if err != nil {
		t.Fatal(err)
	}
	certificate.Key = liveKey
	certificate.Freshness = FreshnessLive
	certificate.CompletedAt = base.Add(-time.Minute)
	if _, ok := EquivalentValidationFinding(job, live, certificate, 24*time.Hour); ok {
		t.Fatal("live validation must never be reused")
	}

	wrong := identity
	wrong.InputHash = "different"
	if _, ok := EquivalentValidationFinding(job, wrong, certificate, 24*time.Hour); ok {
		t.Fatal("mismatched validation identity must fail closed")
	}
}

func TestValidateValidationCertificate(t *testing.T) {
	certificate := ValidationCertificate{
		Key: "key", Result: "success", CompletedAt: time.Now(),
		DefinitionHash: "definition", InputHash: "inputs", ContextHash: "context",
		Freshness: FreshnessContentAddressed,
	}
	if err := ValidateValidationCertificate(certificate); err != nil {
		t.Fatal(err)
	}
	certificate.InputHash = ""
	if err := ValidateValidationCertificate(certificate); err == nil {
		t.Fatal("expected incomplete certificate to fail")
	}
}
