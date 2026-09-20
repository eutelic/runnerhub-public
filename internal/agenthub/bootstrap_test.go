package agenthub

import (
	"context"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestOneTimeEnrollmentGrantAppliesDurableUserTags(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	h, err := New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.IssueEnrollmentToken("fast-box", map[string]string{"fast": "true"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := agent.EnrollRequest{
		Name:     "fast-box",
		Provider: "docker",
		Capabilities: provider.Capabilities{
			Provider:       "docker",
			OS:             "linux",
			Architecture:   "amd64",
			Capacity:       core.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30},
			IsolationModes: []core.IsolationMode{core.IsolationContainerTrusted},
			Labels:         map[string]string{"provider": "docker"},
		},
	}
	enrolled, err := h.Enroll(context.Background(), token, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Enroll(context.Background(), token, req); err == nil {
		t.Fatal("one-time enrollment token was accepted twice")
	}
	host, err := st.GetHost(context.Background(), enrolled.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if host.UserTags["fast"] != "true" || host.Tags["fast"] != "true" {
		t.Fatalf("user tag missing after enrollment: %+v", host)
	}
	if host.SystemTags["isolation.container-trusted"] != "true" {
		t.Fatalf("system isolation tag missing: %+v", host.SystemTags)
	}

	req.Capabilities.Labels = map[string]string{"provider": "docker", "heartbeat": "new"}
	if err := h.Heartbeat(context.Background(), agent.Heartbeat{HostID: enrolled.HostID, Capabilities: req.Capabilities}); err != nil {
		t.Fatal(err)
	}
	host, err = st.GetHost(context.Background(), enrolled.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if host.UserTags["fast"] != "true" || host.Tags["fast"] != "true" {
		t.Fatalf("heartbeat removed user tag: %+v", host)
	}
	if host.Tags["heartbeat"] != "new" {
		t.Fatalf("heartbeat did not refresh system capabilities: %+v", host.Tags)
	}
}
