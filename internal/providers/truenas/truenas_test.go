package truenas

import "testing"

func TestProviderRequiresExplicitDatasetRoot(t *testing.T) {
	if _, err := newWithRunner(Config{}, nil); err == nil {
		t.Fatal("expected empty TrueNAS root to be rejected")
	}
}

func TestProviderRejectsRelativeDatasetRoot(t *testing.T) {
	if _, err := newWithRunner(Config{Root: "runnerhub"}, nil); err == nil {
		t.Fatal("expected relative TrueNAS root to be rejected")
	}
}
