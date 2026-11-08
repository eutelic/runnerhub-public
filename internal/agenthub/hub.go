package agenthub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/store"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ResultHandler func(context.Context, string, agent.CommandResult) error
type enrollmentGrant struct {
	Name      string
	Tags      map[string]string
	ExpiresAt time.Time
}
type Hub struct {
	store            store.Store
	enrollSecret     []byte
	agentSecret      []byte
	aead             cipher.AEAD
	mu               sync.Mutex
	caps             map[string]agent.HostView
	enrollmentGrants map[string]enrollmentGrant
	onResult         ResultHandler
	now              func() time.Time
	lease            time.Duration
}

func New(st store.Store, enrollmentSecret, agentSecret string) (*Hub, error) {
	if st == nil {
		return nil, errors.New("store required")
	}
	if len(enrollmentSecret) < 16 || len(agentSecret) < 32 {
		return nil, errors.New("enrollment secret must be >=16 chars and agent secret >=32 chars")
	}
	key := sha256.Sum256([]byte("runnerhub/durable-command/v1\x00" + agentSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Hub{store: st, enrollSecret: []byte(enrollmentSecret), agentSecret: []byte(agentSecret), aead: aead, caps: map[string]agent.HostView{}, enrollmentGrants: map[string]enrollmentGrant{}, now: time.Now, lease: time.Minute}, nil
}
func (h *Hub) SetResultHandler(fn ResultHandler) { h.onResult = fn }
func (h *Hub) IssueEnrollmentToken(name string, tags map[string]string, ttl time.Duration) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("host name required")
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		ttl = 5 * time.Minute
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for k, g := range h.enrollmentGrants {
		if !g.ExpiresAt.After(now) {
			delete(h.enrollmentGrants, k)
		}
	}
	h.enrollmentGrants[token] = enrollmentGrant{Name: name, Tags: cloneLabels(tags), ExpiresAt: now.Add(ttl)}
	return token, nil
}
func (h *Hub) consumeEnrollmentGrant(token string) (enrollmentGrant, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	g, ok := h.enrollmentGrants[token]
	if !ok {
		return enrollmentGrant{}, false
	}
	delete(h.enrollmentGrants, token)
	if !g.ExpiresAt.After(h.now()) {
		return enrollmentGrant{}, false
	}
	return g, true
}
func capabilityTags(c provider.Capabilities) map[string]string {
	tags := cloneLabels(c.Labels)
	for _, m := range c.IsolationModes {
		if tags == nil {
			tags = map[string]string{}
		}
		tags["isolation."+string(m)] = "true"
	}
	for name, enabled := range c.Features {
		if !enabled {
			continue
		}
		if tags == nil {
			tags = map[string]string{}
		}
		tags["feature."+name] = "true"
	}
	return tags
}
func (h *Hub) Enroll(ctx context.Context, presented string, r agent.EnrollRequest) (agent.EnrollResponse, error) {
	grant := enrollmentGrant{}
	global := hmac.Equal([]byte(presented), h.enrollSecret)
	if !global {
		var ok bool
		grant, ok = h.consumeEnrollmentGrant(presented)
		if !ok {
			return agent.EnrollResponse{}, errors.New("invalid enrollment token")
		}
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return agent.EnrollResponse{}, errors.New("host name required")
	}
	r.PlacementDomainID = strings.TrimSpace(r.PlacementDomainID)
	if len(r.PlacementDomainID) > 128 {
		return agent.EnrollResponse{}, errors.New("placement domain ID must be 128 characters or fewer")
	}
	if !global && grant.Name != "" && grant.Name != r.Name {
		return agent.EnrollResponse{}, errors.New("enrollment token is bound to a different host name")
	}
	id := "host-" + randomID()
	systemTags := capabilityTags(r.Capabilities)
	userTags := cloneLabels(grant.Tags)
	host := core.Host{ID: id, Name: r.Name, Provider: r.Provider, PlacementDomainID: r.PlacementDomainID, State: core.HostOnline, OS: r.Capabilities.OS, Architecture: r.Capabilities.Architecture, SystemTags: systemTags, UserTags: userTags, Tags: core.MergeHostTags(systemTags, userTags), Capacity: r.Capabilities.Capacity, AgentVersion: r.AgentVersion, LastSeenAt: h.now().UTC()}
	if err := h.store.UpsertHost(ctx, host); err != nil {
		return agent.EnrollResponse{}, err
	}
	h.mu.Lock()
	h.caps[id] = agent.HostView{Host: host, Capabilities: r.Capabilities}
	h.mu.Unlock()
	return agent.EnrollResponse{HostID: id, AgentToken: h.token(id, h.now().Add(24*time.Hour)), PollIntervalSeconds: 2}, nil
}
func (h *Hub) Authenticate(hostID, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || h.now().Unix() > exp {
		return false
	}
	if !hmac.Equal([]byte(parts[1]), []byte(h.tokenSignature(hostID, exp))) {
		return false
	}
	host, err := h.store.GetHost(context.Background(), hostID)
	return err == nil && host.State != core.HostArchived
}
func (h *Hub) RefreshToken(hostID string) string { return h.token(hostID, h.now().Add(24*time.Hour)) }
func (h *Hub) Heartbeat(ctx context.Context, b agent.Heartbeat) error {
	host, err := h.store.GetHost(ctx, b.HostID)
	if err != nil {
		return err
	}
	if host.State == core.HostArchived {
		return errors.New("archived host cannot heartbeat")
	}
	host.LastSeenAt = h.now().UTC()
	if host.State != core.HostDraining {
		host.State = core.HostOnline
	}
	host.OS = b.Capabilities.OS
	host.Architecture = b.Capabilities.Architecture
	host.Capacity = b.Capabilities.Capacity
	if strings.TrimSpace(b.AgentVersion) != "" {
		host.AgentVersion = b.AgentVersion
	}
	host.SystemTags = capabilityTags(b.Capabilities)
	host.Tags = core.MergeHostTags(host.SystemTags, host.UserTags)
	if err := h.store.UpsertHost(ctx, host); err != nil {
		return err
	}
	h.mu.Lock()
	h.caps[b.HostID] = agent.HostView{Host: host, Capabilities: b.Capabilities}
	h.mu.Unlock()
	return nil
}
func (h *Hub) Enqueue(hostID string, c agent.Command) error {
	if c.ID == "" {
		c.ID = randomID()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = h.now().UTC()
	}
	sealed, err := h.sealCommand(c)
	if err != nil {
		return err
	}
	return h.store.EnqueueCommand(context.Background(), hostID, sealed)
}
func (h *Hub) Next(hostID string) []agent.Command {
	now := h.now().UTC()
	leased, err := h.store.LeaseCommands(context.Background(), hostID, now, now.Add(h.lease), 20)
	if err != nil {
		return nil
	}
	out := make([]agent.Command, 0, len(leased))
	for _, c := range leased {
		opened, err := h.openCommand(c)
		if err == nil {
			out = append(out, opened)
			continue
		}
		safe := "durable command payload could not be decrypted"
		if h.onResult != nil {
			_ = h.onResult(context.Background(), hostID, agent.CommandResult{CommandID: c.ID, WorkerID: c.WorkerID, OK: false, SafeError: safe, At: now})
		}
		_ = h.store.AppendAudit(context.Background(), core.AuditEvent{Actor: "agenthub", Action: "decrypt_command", ResourceType: "worker", ResourceID: c.WorkerID, Result: "error", SafeDetail: safe, CreatedAt: now})
		_ = h.store.AckCommand(context.Background(), c.ID, now)
	}
	return out
}
func (h *Hub) HandleResult(ctx context.Context, hostID string, r agent.CommandResult) error {
	if strings.TrimSpace(r.CommandID) == "" {
		return errors.New("command id required")
	}
	rec, err := h.store.GetCommand(ctx, r.CommandID)
	if err != nil {
		return err
	}
	if rec.HostID != hostID || rec.Command.WorkerID != r.WorkerID {
		return errors.New("command result identity mismatch")
	}
	if !rec.AckedAt.IsZero() {
		return nil
	}
	if h.onResult != nil {
		if err := h.onResult(ctx, hostID, r); err != nil {
			return err
		}
	}
	return h.store.AckCommand(ctx, r.CommandID, h.now().UTC())
}
func (h *Hub) MarkStale(ctx context.Context, maxAge time.Duration) error {
	hosts, err := h.store.ListHosts(ctx)
	if err != nil {
		return err
	}
	now := h.now()
	for _, host := range hosts {
		if host.State == core.HostDraining {
			continue
		}
		if host.LastSeenAt.IsZero() || now.Sub(host.LastSeenAt) > maxAge {
			host.State = core.HostOffline
			if err := h.store.UpsertHost(ctx, host); err != nil {
				return err
			}
		}
	}
	return nil
}
func (h *Hub) sealCommand(c agent.Command) (agent.Command, error) {
	if c.Spec == nil || c.Spec.JITConfig == "" {
		return c, nil
	}
	spec := *c.Spec
	plaintext := []byte(spec.JITConfig)
	nonce := make([]byte, h.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return agent.Command{}, err
	}
	sealed := h.aead.Seal(nil, nonce, plaintext, []byte(c.ID))
	payload := append(nonce, sealed...)
	spec.JITConfig = "enc:v1:" + base64.RawURLEncoding.EncodeToString(payload)
	c.Spec = &spec
	return c, nil
}
func (h *Hub) openCommand(c agent.Command) (agent.Command, error) {
	if c.Spec == nil || !strings.HasPrefix(c.Spec.JITConfig, "enc:v1:") {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(c.Spec.JITConfig, "enc:v1:"))
	if err != nil {
		return agent.Command{}, errors.New("decode encrypted command")
	}
	n := h.aead.NonceSize()
	if len(raw) <= n {
		return agent.Command{}, errors.New("encrypted command is truncated")
	}
	plain, err := h.aead.Open(nil, raw[:n], raw[n:], []byte(c.ID))
	if err != nil {
		return agent.Command{}, errors.New("decrypt command")
	}
	spec := *c.Spec
	spec.JITConfig = string(plain)
	c.Spec = &spec
	return c, nil
}
func (h *Hub) token(id string, expires time.Time) string {
	exp := expires.Unix()
	return strconv.FormatInt(exp, 10) + "." + h.tokenSignature(id, exp)
}
func (h *Hub) tokenSignature(id string, exp int64) string {
	m := hmac.New(sha256.New, h.agentSecret)
	_, _ = m.Write([]byte(id + "." + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func cloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
