// Package mcpaccess manages administrator-approved, scoped MCP connections.
// Client secrets never persist on the server: only their SHA-256 digests do.
package mcpaccess

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pendingTTL = 10 * time.Minute
	maxPending = 40
	maxActive  = 100
)

type Entry struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	RequestID string    `json:"request_id"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires,omitempty"`
	Approved  bool      `json:"approved"`
}
type persisted struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}
type Registry struct {
	mu       sync.Mutex
	path     string
	entries  map[string]Entry
	attempts map[string][]time.Time
}

func New(path string) (*Registry, error) {
	r := &Registry{path: path, entries: make(map[string]Entry), attempts: make(map[string][]time.Time)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot persisted
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid MCP grants: %w", err)
	}
	if snapshot.Version != 1 || snapshot.Entries == nil {
		return nil, errors.New("unsupported MCP grants format")
	}
	r.entries = snapshot.Entries
	return r, nil
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validToken(token string) bool {
	if len(token) < 40 || len(token) > 128 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil
}

func (r *Registry) lookupLocked(token string) (Entry, bool) {
	if !validToken(token) {
		return Entry{}, false
	}
	e, ok := r.entries[tokenDigest(token)]
	if !ok || (!e.Approved && !e.Expires.After(time.Now())) {
		return Entry{}, false
	}
	return e, true
}

// Status never reveals another client's name, key, or grant.
func (r *Registry) Status(token string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.lookupLocked(token)
	if !ok {
		return "unregistered"
	}
	if e.Approved {
		return "approved"
	}
	return "pending"
}

// Lookup validates the grant on each API call so revocation is immediate.
func (r *Registry) Lookup(token string) (name, scope string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.lookupLocked(token)
	if !ok || !e.Approved || (e.Scope != "viewer" && e.Scope != "operator") {
		return "", "", false
	}
	return e.Name, e.Scope, true
}

func (r *Registry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(r.path), ".mcp-grants-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if err := json.NewEncoder(f).Encode(persisted{Version: 1, Entries: r.entries}); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), r.path)
}

func (r *Registry) purgeLocked(now time.Time) {
	for key, e := range r.entries {
		if !e.Approved && !e.Expires.After(now) {
			delete(r.entries, key)
		}
	}
}

func (r *Registry) Request(token, name, source string) (string, error) {
	if !validToken(token) || len(name) == 0 || len(name) > 80 || strings.TrimSpace(name) == "" {
		return "", errors.New("invalid MCP client token or name")
	}
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.lookupLocked(token); ok {
		if e.Approved {
			return "approved", nil
		}
		return "pending", nil
	}
	now := time.Now()
	r.purgeLocked(now)
	// Prevent an unauthenticated source from exhausting the approval queue.
	kept := r.attempts[source][:0]
	for _, t := range r.attempts[source] {
		if now.Sub(t) < 5*time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 8 {
		return "", errors.New("too many connection requests")
	}
	r.attempts[source] = append(kept, now)
	pending, active := 0, 0
	for _, e := range r.entries {
		if e.Approved {
			active++
		} else {
			pending++
		}
	}
	if pending >= maxPending || active >= maxActive {
		return "", errors.New("MCP connection limit reached")
	}
	requestIDBytes := make([]byte, 24)
	if _, err := rand.Read(requestIDBytes); err != nil {
		return "", err
	}
	digest := tokenDigest(token)
	r.entries[digest] = Entry{
		Name: name, RequestID: base64.RawURLEncoding.EncodeToString(requestIDBytes),
		Created: now, Expires: now.Add(pendingTTL),
	}
	if err := r.saveLocked(); err != nil {
		delete(r.entries, digest)
		return "", err
	}
	return "pending", nil
}

func (r *Registry) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked(time.Now())
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	// Small bounded collection; deterministic newest-first dashboard.
	sortEntries(out)
	return out
}

func (r *Registry) Decide(id, action string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLocked(time.Now())
	for digest, e := range r.entries {
		if e.RequestID != id || id == "" {
			continue
		}
		old := e
		switch action {
		case "approve-viewer", "approve-operator":
			if e.Approved {
				return errors.New("already approved")
			}
			e.Approved = true
			e.Scope = strings.TrimPrefix(action, "approve-")
			e.Expires = time.Time{}
			r.entries[digest] = e
		case "deny":
			if e.Approved {
				return errors.New("cannot deny an approved connection")
			}
			delete(r.entries, digest)
		case "revoke":
			if !e.Approved {
				return errors.New("connection is not approved")
			}
			delete(r.entries, digest)
		default:
			return errors.New("invalid action")
		}
		if err := r.saveLocked(); err != nil {
			r.entries[digest] = old
			return err
		}
		return nil
	}
	return errors.New("unknown or expired request")
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Created.After(entries[j].Created) })
}
