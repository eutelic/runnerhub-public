package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

func TestCIValidationCertificateRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	identity := ciaudit.ValidationIdentity{
		DefinitionHash: "definition",
		InputHash:      "inputs",
		ContextHash:    "context",
		Freshness:      ciaudit.FreshnessContentAddressed,
	}
	key, err := identity.Key()
	if err != nil {
		t.Fatal(err)
	}
	certificate := ciaudit.ValidationCertificate{
		Key: key, Result: "success", CompletedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		DefinitionHash: identity.DefinitionHash, InputHash: identity.InputHash,
		ContextHash: identity.ContextHash, Freshness: identity.Freshness,
		ArtifactDigests: []string{"sha256:abc"},
	}
	if err := st.UpsertCIValidationCertificate(ctx, "Eutelic/RunnerHub", certificate); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetCIValidationCertificate(ctx, "eutelic/runnerhub", key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != certificate.Key || got.Result != "success" || len(got.ArtifactDigests) != 1 {
		t.Fatalf("certificate=%+v", got)
	}

	certificate.ArtifactDigests = []string{"sha256:def"}
	certificate.CompletedAt = certificate.CompletedAt.Add(time.Minute)
	if err := st.UpsertCIValidationCertificate(ctx, "eutelic/runnerhub", certificate); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetCIValidationCertificate(ctx, "eutelic/runnerhub", key)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArtifactDigests[0] != "sha256:def" {
		t.Fatalf("upsert did not replace certificate: %+v", got)
	}
}

func TestCIValidationCertificateMissing(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.GetCIValidationCertificate(ctx, "eutelic/runnerhub", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}
