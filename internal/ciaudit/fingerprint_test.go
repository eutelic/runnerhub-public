package ciaudit

import "testing"

func TestValidationIdentityKeyIsStable(t *testing.T) {
	v := ValidationIdentity{
		DefinitionHash: "definition",
		InputHash:      "inputs",
		ContextHash:    "context",
		Freshness:      FreshnessContentAddressed,
	}
	first, err := v.Key()
	if err != nil {
		t.Fatal(err)
	}
	second, err := v.Key()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("validation key changed: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("expected sha256 hex key, got %q", first)
	}
}

func TestValidationIdentitySeparatesDefinitionInputAndContext(t *testing.T) {
	base := ValidationIdentity{
		DefinitionHash: "definition",
		InputHash:      "inputs",
		ContextHash:    "context",
		Freshness:      FreshnessContentAddressed,
	}
	want, err := base.Key()
	if err != nil {
		t.Fatal(err)
	}
	variants := []ValidationIdentity{
		{DefinitionHash: "different", InputHash: base.InputHash, ContextHash: base.ContextHash, Freshness: base.Freshness},
		{DefinitionHash: base.DefinitionHash, InputHash: "different", ContextHash: base.ContextHash, Freshness: base.Freshness},
		{DefinitionHash: base.DefinitionHash, InputHash: base.InputHash, ContextHash: "different", Freshness: base.Freshness},
		{DefinitionHash: base.DefinitionHash, InputHash: base.InputHash, ContextHash: base.ContextHash, Freshness: FreshnessLive},
	}
	for _, variant := range variants {
		got, err := variant.Key()
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			t.Fatalf("variant unexpectedly reused validation key: %+v", variant)
		}
	}
}

func TestHashPartsLengthPrefixesComponents(t *testing.T) {
	if HashParts("ab", "c") == HashParts("a", "bc") {
		t.Fatal("ambiguous component boundaries produced the same hash")
	}
}

func TestValidationIdentityRejectsIncompleteIdentity(t *testing.T) {
	if _, err := (ValidationIdentity{DefinitionHash: "definition"}).Key(); err == nil {
		t.Fatal("expected incomplete validation identity to fail")
	}
}
