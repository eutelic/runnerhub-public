package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/machinebootstrap"
	"net/http"
	"strings"
	"time"
)

func (s *Service) hosts(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListHosts(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

type scanSSHHostKeyRequest struct {
	Address string `json:"address"`
}

func (s *Service) scanSSHHostKey(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, 403, errors.New("SSH host-key scan requires admin access"))
		return
	}
	scanner, ok := s.MachineBootstrap.(machinebootstrap.HostKeyScanner)
	if !ok {
		problem(w, 503, errors.New("machine bootstrap does not support SSH host-key scans"))
		return
	}
	var in scanSSHHostKeyRequest
	if !decode(w, r, &in) {
		return
	}
	in.Address = strings.TrimSpace(in.Address)
	if in.Address == "" {
		problem(w, 400, errors.New("machine address is required"))
		return
	}
	fingerprints, err := scanner.ScanHostKey(r.Context(), in.Address)
	if err != nil {
		problem(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"fingerprints": fingerprints})
}

type bootstrapHostRequest struct {
	Address                string   `json:"address"`
	User                   string   `json:"user"`
	Password               string   `json:"password,omitempty"`
	SudoPassword           string   `json:"sudoPassword,omitempty"`
	PrivateKey             string   `json:"privateKey,omitempty"`
	HostKeyFingerprint     string   `json:"hostKeyFingerprint,omitempty"`
	RegistryUsername       string   `json:"registryUsername,omitempty"`
	RegistryPassword       string   `json:"registryPassword,omitempty"`
	Name                   string   `json:"name"`
	Tags                   []string `json:"tags,omitempty"`
	Priority               int      `json:"priority,omitempty"`
	InstallDocker          *bool    `json:"installDocker,omitempty"`
	AllowPrivilegedWorkers bool     `json:"allowPrivilegedWorkers,omitempty"`
	Mode                   string   `json:"mode,omitempty"`
}

func validTagName(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
func tagMap(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if !validTagName(v) {
			return nil, fmt.Errorf("invalid machine tag %q", raw)
		}
		if strings.HasPrefix(strings.ToLower(v), "isolation.") {
			return nil, fmt.Errorf("machine tag %q is reserved", v)
		}
		out[v] = "true"
	}
	return out, nil
}
func (s *Service) bootstrapHost(w http.ResponseWriter, r *http.Request) {
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w, http.StatusForbidden, errors.New("machine bootstrap requires admin access"))
		return
	}
	if s.MachineBootstrap == nil || strings.TrimSpace(s.MachineBootstrapURL) == "" || s.Hub == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("machine bootstrap is not configured"))
		return
	}
	var in bootstrapHostRequest
	if !decode(w, r, &in) {
		return
	}
	if err := validateBootstrapInput(&in); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	// The old synchronous route must participate in deduplication too, so a
	// client using it cannot race the newer tracked operation endpoint.
	opID := ""
	if st, ok := s.operationStore(); ok {
		op, reused, err := st.StartOperation(r.Context(), newHostOperation(in.Name, actor(r.Context())))
		if err != nil {
			problem(w, http.StatusInternalServerError, errors.New("could not record machine enrollment"))
			return
		}
		if reused {
			problem(w, http.StatusConflict, fmt.Errorf("enrollment already in progress; inspect operation %s", op.ID))
			return
		}
		opID = op.ID
	}
	ctx := r.Context()
	if opID != "" {
		s.transitionHostOperation(ctx, opID, "working", "preparing-enrollment",
			"Preparing an enrollment token and checking existing host identities.", "")
		ctx = machinebootstrap.WithProgress(ctx, func(step string) {
			switch step {
			case "verifying-host-key", "configuring-remote-host", "waiting-for-agent", "cleaning-enrollment-secret":
				state := "working"
				if step == "waiting-for-agent" {
					state = "waiting"
				}
				writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				s.transitionHostOperation(writeCtx, opID, state, step, "Remote bootstrap is in progress.", "")
			}
		})
	}
	host, err := s.runHostBootstrap(ctx, in)
	// Request contexts are cancelled when the client disconnects. A fresh,
	// bounded context ensures the final status is still recorded.
	finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err != nil {
		state := "failed"
		stage := "bootstrap-failed"
		detail := "Remote setup did not complete. Inspect host diagnostics before retrying."
		if host.ID != "" {
			state = "blocked"
			stage = "enrollment-cleanup-failed"
			detail = "New agent was observed but final credential cleanup failed; inspect remote agent before retrying."
		} else if ctx.Err() != nil {
			state = "unknown"
			stage = "bootstrap-timed-out"
			detail = "Client disconnected or timed out; the remote outcome is unknown."
		}
		if opID != "" {
			s.transitionHostOperation(finalCtx, opID, state, stage, detail, host.ID)
		}
		_ = s.Store.AppendAudit(finalCtx, core.AuditEvent{
			Actor: actor(r.Context()), Action: "bootstrap_host", ResourceType: "host", ResourceID: host.ID,
			Result: state, SafeDetail: "Host bootstrap did not confirm safe readiness; inspect remote agent logs.",
		})
		detail = "host bootstrap did not confirm readiness; check SSH, Docker and agent logs"
		if in.Mode == "android-emulator" {
			detail = "native Android bootstrap did not confirm hardware.kvm; check SSH, device access for runnerhub-android and agent logs"
		}
		problem(w, http.StatusBadGateway, errors.New(detail))
		return
	}
	if opID != "" {
		s.transitionHostOperation(finalCtx, opID, "succeeded", "agent-ready",
			"New agent identity confirmed. Review previous identities separately.", host.ID)
	}
	_ = s.Store.AppendAudit(finalCtx, core.AuditEvent{
		Actor: actor(r.Context()), Action: "bootstrap_host", ResourceType: "host",
		ResourceID: host.ID, Result: "ok", SafeDetail: in.Name,
	})
	writeJSON(w, http.StatusCreated, host)
}
func (s *Service) drain(w http.ResponseWriter, r *http.Request) {
	h, err := s.Store.GetHost(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	if h.State == core.HostArchived {
		problem(w, 409, errors.New("archived hosts cannot be drained"))
		return
	}
	if h.State == core.HostOnline {
		h.State = core.HostDraining
	}
	if err := s.Store.UpsertHost(r.Context(), h); err != nil {
		problem(w, 500, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "drain_host", ResourceType: "host", ResourceID: h.ID, Result: "ok"})
	writeJSON(w, 200, h)
}

type updateHostRequest struct {
	Name              *string           `json:"name,omitempty"`
	PlacementDomainID *string           `json:"placementDomainId,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	Priority          *int              `json:"priority,omitempty"`
	State             *core.HostState   `json:"state,omitempty"`
}

func (s *Service) updateHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.Store.GetHost(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	if h.State == core.HostArchived {
		problem(w, 409, errors.New("archived hosts cannot be edited"))
		return
	}
	var in updateHostRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		h.Name = strings.TrimSpace(*in.Name)
	}
	if in.PlacementDomainID != nil {
		domain := strings.TrimSpace(*in.PlacementDomainID)
		if len(domain) > 128 {
			problem(w, 400, errors.New("placement domain ID must be 128 characters or fewer"))
			return
		}
		h.PlacementDomainID = domain
	}
	if in.Tags != nil {
		for k := range in.Tags {
			if !validTagName(k) || strings.HasPrefix(strings.ToLower(k), "isolation.") {
				problem(w, 400, fmt.Errorf("invalid or reserved machine tag %q", k))
				return
			}
		}
		h.UserTags = map[string]string{}
		for k, v := range in.Tags {
			h.UserTags[k] = strings.TrimSpace(v)
			if h.UserTags[k] == "" {
				h.UserTags[k] = "true"
			}
		}
		if h.SystemTags == nil {
			h.SystemTags = map[string]string{}
			for k, v := range h.Tags {
				if strings.HasPrefix(strings.ToLower(k), "isolation.") {
					h.SystemTags[k] = v
				}
			}
		}
		h.Tags = core.MergeHostTags(h.SystemTags, h.UserTags)
	}
	if in.Priority != nil {
		if *in.Priority < -1000 || *in.Priority > 1000 {
			problem(w, 400, errors.New("host priority must be between -1000 and 1000"))
			return
		}
		h.Priority = *in.Priority
	}
	if in.State != nil {
		if !core.CanTransitionHost(h.State, *in.State) {
			problem(w, 409, fmt.Errorf("invalid host transition %s -> %s", h.State, *in.State))
			return
		}
		h.State = *in.State
	}
	if err := s.Store.UpsertHost(r.Context(), h); err != nil {
		problem(w, 500, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "update_host", ResourceType: "host", ResourceID: h.ID, Result: "ok"})
	writeJSON(w, 200, h)
}
func (s *Service) bulkDrain(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Store.ListHosts(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	n := 0
	for _, h := range hosts {
		if h.State == core.HostOnline {
			h.State = core.HostDraining
			if s.Store.UpsertHost(r.Context(), h) == nil {
				n++
			}
		}
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "bulk_drain", ResourceType: "host", Result: "ok", SafeDetail: fmt.Sprintf("%d hosts", n)})
	writeJSON(w, 200, map[string]int{"drained": n})
}

type imageUpdateRequest struct {
	Image         string `json:"image"`
	RequireDigest bool   `json:"requireDigest"`
}

func (s *Service) updatePoolImage(w http.ResponseWriter, r *http.Request) {
	s.poolMutationMu.Lock()
	defer s.poolMutationMu.Unlock()
	p, err := s.Store.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, err)
		return
	}
	before := p
	var in imageUpdateRequest
	if !decode(w, r, &in) {
		return
	}
	if in.RequireDigest {
		if err := images.ValidatePinned(in.Image); err != nil {
			problem(w, 400, err)
			return
		}
	}
	p.Image = strings.TrimSpace(in.Image)
	resetLearningForConfigChange(&before, &p)
	if err := core.ValidateRunnerPool(p); err != nil {
		problem(w, 400, err)
		return
	}
	if err := s.Policy.ValidatePool(p); err != nil {
		problem(w, 403, err)
		return
	}
	if err := s.Store.UpsertPool(r.Context(), p); err != nil {
		problem(w, 500, err)
		return
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "update_pool_image", ResourceType: "pool", ResourceID: p.ID, Result: "ok", SafeDetail: p.Image})
	writeJSON(w, 200, p)
}
