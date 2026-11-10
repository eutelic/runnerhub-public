//go:build !localtest

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestHostSchedulingTogglePersistsAcrossHostUpsertsAndRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runnerhub.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	host := core.Host{ID: "host-1", Name: "compute", State: core.HostOnline}
	if err := st.UpsertHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if initial, err := st.GetHost(ctx, host.ID); err != nil || initial.SchedulingDisabled {
		t.Fatalf("new hosts should default enabled: %+v %v", initial, err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, host.ID, true); err != nil {
		t.Fatal(err)
	}
	// A previously read heartbeat cannot overwrite an operator's toggle.
	host.Name = "compute-updated"
	host.State = core.HostOffline
	if err := st.UpsertHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetHost(ctx, host.ID)
	if err != nil || !got.SchedulingDisabled || got.Name != "compute-updated" {
		t.Fatalf("upsert erased scheduling flag: %+v %v", got, err)
	}
	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 || !hosts[0].SchedulingDisabled {
		t.Fatalf("list omitted flag: %+v %v", hosts, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err = st.GetHost(ctx, host.ID)
	if err != nil || !got.SchedulingDisabled {
		t.Fatalf("restart lost toggle: %+v %v", got, err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, host.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetHost(ctx, host.ID)
	if err != nil || got.SchedulingDisabled {
		t.Fatalf("enable failed: %+v %v", got, err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	got.State = core.HostArchived
	if err := st.UpsertHost(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, host.ID, true); !errors.Is(err, ErrHostArchived) {
		t.Fatalf("archived host should be immutable: %v", err)
	}
}
