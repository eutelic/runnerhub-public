package deployer

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const defaultTimeout = 2 * time.Minute

var targetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Target struct {
	ID             string `json:"id"`
	Repository     string `json:"repository"`
	App            string `json:"app"`
	Image          string `json:"image"`
	TokenSHA256    string `json:"token_sha256"`
	HealthURL      string `json:"health_url,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (t Target) Timeout() time.Duration {
	if t.TimeoutSeconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(t.TimeoutSeconds) * time.Second
}

type Policy struct {
	Targets []Target `json:"targets"`
}

type policyTarget struct {
	Target
	tokenHash [sha256.Size]byte
}

type PolicySet struct {
	targets map[string]policyTarget
}

func LoadPolicy(path string) (*PolicySet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read deploy policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse deploy policy: %w", err)
	}
	return NewPolicySet(p)
}

func NewPolicySet(p Policy) (*PolicySet, error) {
	if len(p.Targets) == 0 {
		return nil, errors.New("deploy policy must contain at least one target")
	}
	out := &PolicySet{targets: make(map[string]policyTarget, len(p.Targets))}
	for _, target := range p.Targets {
		target.ID = strings.TrimSpace(target.ID)
		target.Repository = strings.TrimSpace(target.Repository)
		target.App = strings.TrimSpace(target.App)
		target.Image = strings.TrimSpace(target.Image)
		target.TokenSHA256 = strings.ToLower(strings.TrimSpace(target.TokenSHA256))
		target.HealthURL = strings.TrimSpace(target.HealthURL)

		if !targetIDPattern.MatchString(target.ID) {
			return nil, fmt.Errorf("target id %q is invalid", target.ID)
		}
		if target.Repository == "" || !strings.Contains(target.Repository, "/") {
			return nil, fmt.Errorf("target %q repository must be owner/name", target.ID)
		}
		if target.App == "" {
			return nil, fmt.Errorf("target %q app is required", target.ID)
		}
		if target.Image == "" {
			return nil, fmt.Errorf("target %q image is required for deployment audit", target.ID)
		}
		if _, exists := out.targets[target.ID]; exists {
			return nil, fmt.Errorf("target id %q is duplicated", target.ID)
		}
		hashBytes, err := hex.DecodeString(target.TokenSHA256)
		if err != nil || len(hashBytes) != sha256.Size {
			return nil, fmt.Errorf("target %q token_sha256 must be a 64-character SHA-256 hex digest", target.ID)
		}
		var hash [sha256.Size]byte
		copy(hash[:], hashBytes)

		if target.HealthURL != "" {
			u, err := url.Parse(target.HealthURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("target %q health_url must be an absolute http(s) URL", target.ID)
			}
			if u.User != nil {
				return nil, fmt.Errorf("target %q health_url must not contain credentials", target.ID)
			}
		}
		if target.TimeoutSeconds < 0 || target.TimeoutSeconds > 900 {
			return nil, fmt.Errorf("target %q timeout_seconds must be between 1 and 900 when set", target.ID)
		}
		out.targets[target.ID] = policyTarget{Target: target, tokenHash: hash}
	}
	return out, nil
}

func (p *PolicySet) Authenticate(id, token string) (Target, bool) {
	if p == nil {
		return Target{}, false
	}
	target, ok := p.targets[id]
	if !ok || token == "" {
		return Target{}, false
	}
	sum := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(sum[:], target.tokenHash[:]) != 1 {
		return Target{}, false
	}
	return target.Target, true
}
