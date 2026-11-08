package images

import "testing"

func TestPinnedImage(t *testing.T) {
	d := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := ValidatePinned("registry.example:5000/team/runner@sha256:" + d); err != nil {
		t.Fatal(err)
	}
	if ValidatePinned("ghcr.io/actions/actions-runner:latest") == nil {
		t.Fatal("tag-only image must not be accepted as pinned")
	}
}

func TestDefaultRunnerImageIsImmutable(t *testing.T) {
	if err := ValidatePinned(DefaultRunnerImage); err != nil {
		t.Fatalf("default runner image is not pinned: %v", err)
	}
}

func TestUpgradeLegacyDefault(t *testing.T) {
	got, changed := UpgradeLegacyDefault(LegacyDefaultRunnerImage)
	if !changed || got != DefaultRunnerImage {
		t.Fatalf("got=%q changed=%v", got, changed)
	}
	custom := "ghcr.io/acme/custom:latest"
	got, changed = UpgradeLegacyDefault(custom)
	if changed || got != custom {
		t.Fatalf("custom image was unexpectedly migrated: got=%q changed=%v", got, changed)
	}
}
