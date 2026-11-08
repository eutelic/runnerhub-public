package ciaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const AnalyzerVersion = "ci-efficiency-v1"

type FreshnessClass string

const (
	FreshnessContentAddressed FreshnessClass = "content-addressed"
	FreshnessExternal         FreshnessClass = "external"
	FreshnessLive             FreshnessClass = "live"
)

type ValidationIdentity struct {
	AnalyzerVersion string         `json:"analyzerVersion"`
	DefinitionHash  string         `json:"definitionHash"`
	InputHash       string         `json:"inputHash"`
	ContextHash     string         `json:"contextHash"`
	Freshness       FreshnessClass `json:"freshness"`
}

// Key returns a stable validation identity. Length-prefixing prevents ambiguous
// concatenations such as ("ab", "c") and ("a", "bc") from hashing alike.
func (v ValidationIdentity) Key() (string, error) {
	version := strings.TrimSpace(v.AnalyzerVersion)
	if version == "" {
		version = AnalyzerVersion
	}
	definition := strings.TrimSpace(v.DefinitionHash)
	input := strings.TrimSpace(v.InputHash)
	context := strings.TrimSpace(v.ContextHash)
	freshness := strings.TrimSpace(string(v.Freshness))
	if definition == "" || input == "" || context == "" || freshness == "" {
		return "", errors.New("validation identity requires definition, input, context, and freshness")
	}
	return HashParts(version, definition, input, context, freshness), nil
}

type ValidationCertificate struct {
	Key             string         `json:"key"`
	Result          string         `json:"result"`
	CompletedAt     time.Time      `json:"completedAt"`
	DefinitionHash  string         `json:"definitionHash"`
	InputHash       string         `json:"inputHash"`
	ContextHash     string         `json:"contextHash"`
	Freshness       FreshnessClass `json:"freshness"`
	ArtifactDigests []string       `json:"artifactDigests,omitempty"`
}

// HashParts provides a deterministic content hash for already-normalized
// components. Callers are responsible for semantic normalization before using
// this function for workflow definitions, repository inputs, or context.
func HashParts(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		value := []byte(part)
		var size [8]byte
		n := uint64(len(value))
		for i := 7; i >= 0; i-- {
			size[i] = byte(n)
			n >>= 8
		}
		_, _ = h.Write(size[:])
		_, _ = h.Write(value)
	}
	return hex.EncodeToString(h.Sum(nil))
}
