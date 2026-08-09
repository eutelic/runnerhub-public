package agenthub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/agent"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/store"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ResultHandler func(context.Context, string, agent.CommandResult) error
type Hub struct {
	store        store.Store
	enrollSecret []byte
	agentSecret  []byte
	mu           sync.Mutex
	queues       map[string][]agent.Command
	caps         map[string]agent.HostView
	onResult     ResultHandler
	now          func() time.Time
}

func New(st store.Store, enrollmentSecret, agentSecret string) (*Hub, error) {
	if st == nil {
		return nil, errors.New("store required")
	}
	if len(enrollmentSecret) < 16 || len(agentSecret) < 32 {
		return nil, errors.New("enrollment secret must be >=16 chars and agent secret >=32 chars")
	}
	return &Hub{store: st, enrollSecret: []byte(enrollmentSecret), agentSecret: []byte(agentSecret), queues: map[string][]agent.Command{}, caps: map[string]agent.HostView{}, now: time.Now}, nil
}
func (h *Hub) SetResultHandler(fn ResultHandler) { h.onResult = fn }
func (h *Hub) Enroll(ctx context.Context, presented string, r agent.EnrollRequest) (agent.EnrollResponse, error) {
	if !hmac.Equal([]byte(presented), h.enrollSecret) {
		return agent.EnrollResponse{}, errors.New("invalid enrollment token")
	}
	if strings.TrimSpace(r.Name) == "" {
		return agent.EnrollResponse{}, errors.New("host name required")
	}
	id := stableHostID(r.Name)
	host := core.Host{ID: id, Name: r.Name, Provider: r.Provider, State: core.HostOnline, OS: r.Capabilities.OS, Architecture: r.Capabilities.Architecture, Tags: cloneLabels(r.Capabilities.Labels), Capacity: r.Capabilities.Capacity, AgentVersion: r.AgentVersion, LastSeenAt: h.now().UTC()}
	for _, m := range r.Capabilities.IsolationModes {
		if host.Tags == nil {
			host.Tags = map[string]string{}
		}
		host.Tags["isolation."+string(m)] = "true"
	}
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
	return hmac.Equal([]byte(parts[1]), []byte(h.tokenSignature(hostID, exp)))
}
func (h *Hub) RefreshToken(hostID string) string { return h.token(hostID, h.now().Add(24*time.Hour)) }
func (h *Hub) Heartbeat(ctx context.Context, b agent.Heartbeat) error {
	host, err := h.store.GetHost(ctx, b.HostID)
	if err != nil {
		return err
	}
	host.LastSeenAt = h.now().UTC()
	if host.State != core.HostDraining {
		host.State = core.HostOnline
	}
	host.OS = b.Capabilities.OS
	host.Architecture = b.Capabilities.Architecture
	host.Capacity = b.Capabilities.Capacity
	host.Tags = cloneLabels(b.Capabilities.Labels)
	for _, m := range b.Capabilities.IsolationModes {
		if host.Tags == nil {
			host.Tags = map[string]string{}
		}
		host.Tags["isolation."+string(m)] = "true"
	}
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
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queues[hostID] = append(h.queues[hostID], c)
	return nil
}
func (h *Hub) Next(hostID string) []agent.Command {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queues[hostID]
	if len(q) == 0 {
		return nil
	}
	out := append([]agent.Command(nil), q...)
	delete(h.queues, hostID)
	return out
}
func (h *Hub) HandleResult(ctx context.Context, hostID string, r agent.CommandResult) error {
	if h.onResult != nil {
		return h.onResult(ctx, hostID, r)
	}
	return nil
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
func (h *Hub) token(id string, expires time.Time) string {
	exp := expires.Unix()
	return strconv.FormatInt(exp, 10) + "." + h.tokenSignature(id, exp)
}
func (h *Hub) tokenSignature(id string, exp int64) string {
	m := hmac.New(sha256.New, h.agentSecret)
	_, _ = m.Write([]byte(id + "." + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func stableHostID(name string) string {
	s := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	return fmt.Sprintf("host-%x", s[:6])
}
func randomID() string {
	b := make([]byte, 8)
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
