package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestDisabledHostIsExcludedFromPlacementAndCapacity(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	putHost(t, st, "disabled")
	putHost(t, st, "enabled")
	h, err := st.GetHost(ctx, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	h.Priority = 100
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, "disabled", true); err != nil {
		t.Fatal(err)
	}
	pool := testPool()
	decision, err := (Scheduler{Store: st}).SelectHost(ctx, pool)
	if err != nil || decision.Host.ID != "enabled" {
		t.Fatalf("disabled host selected: %+v %v", decision, err)
	}
	all, err := st.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeCapacity(pool, all, nil)
	if summary.CompatibleHosts != 1 {
		t.Fatalf("wrong eligible host count: %+v", summary)
	}
	for _, host := range all {
		if host.ID == "disabled" && !strings.Contains(HostRejectionReason(host, pool, core.Resources{}), "disabled") {
			t.Fatalf("disabled host lacks rejection reason: %+v", host)
		}
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, "enabled", true); err != nil {
		t.Fatal(err)
	}
	if _, err := (Scheduler{Store: st}).SelectHost(ctx, pool); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("all hosts disabled, should be no capacity: %v", err)
	}
	if _, err := st.SetHostSchedulingDisabled(ctx, "disabled", false); err != nil {
		t.Fatal(err)
	}
	decision, err = (Scheduler{Store: st}).SelectHost(ctx, pool)
	if err != nil || decision.Host.ID != "disabled" {
		t.Fatalf("re-enable failed: %+v %v", decision, err)
	}
}
