package agenthub

import (
	"context"
	"github.com/example-user/runnerhub/internal/agent"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"github.com/example-user/runnerhub/internal/store"
	"testing"
	"time"
)

func TestHeartbeatPreservesDrainAndRotatingToken(t *testing.T) {
	st, _ := store.OpenSQLite("")
	defer st.Close()
	h, _ := New(st, "1234567890123456", "12345678901234567890123456789012")
	now := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	e, err := h.Enroll(context.Background(), "1234567890123456", agent.EnrollRequest{Name: "host", Provider: "docker", Capabilities: provider.Capabilities{OS: "linux", IsolationModes: []core.IsolationMode{core.IsolationContainerTrusted}}})
	if err != nil {
		t.Fatal(err)
	}
	host, _ := st.GetHost(context.Background(), e.HostID)
	host.State = core.HostDraining
	_ = st.UpsertHost(context.Background(), host)
	if !h.Authenticate(e.HostID, e.AgentToken) {
		t.Fatal("fresh token rejected")
	}
	if err := h.Heartbeat(context.Background(), agent.Heartbeat{HostID: e.HostID, Capabilities: provider.Capabilities{OS: "linux"}}); err != nil {
		t.Fatal(err)
	}
	host, _ = st.GetHost(context.Background(), e.HostID)
	if host.State != core.HostDraining {
		t.Fatalf("drain lost: %s", host.State)
	}
	now = now.Add(25 * time.Hour)
	if h.Authenticate(e.HostID, e.AgentToken) {
		t.Fatal("expired token accepted")
	}
}
