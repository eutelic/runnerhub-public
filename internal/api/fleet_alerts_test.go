package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestServerFleetAlertsRequireSustainedEvidenceAndPersistResolution(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runnerhub.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.UpsertHost(ctx, core.Host{ID: "host-1", Name: "compute", Provider: "docker", State: core.HostOffline, LastSeenAt: now.Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	if err := svc.reconcileFleetAlertsAt(ctx, now); err != nil {
		t.Fatal(err)
	}
	alert, err := st.GetFleetAlert(ctx, "host:host-1")
	if err != nil {
		t.Fatal(err)
	}
	if alert.State != "observing" || !alert.FirstSeenAt.Equal(now) {
		t.Fatalf("first sustained observation=%+v", alert)
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "host:host-1")
	if err != nil {
		t.Fatal(err)
	}
	if alert.State != "active" || !alert.FirstSeenAt.Equal(now) {
		t.Fatalf("sustained alert=%+v", alert)
	}

	if err := st.UpsertHost(ctx, core.Host{ID: "host-1", Name: "compute", Provider: "docker", State: core.HostOnline, LastSeenAt: now.Add(7 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "host:host-1")
	if err != nil {
		t.Fatal(err)
	}
	if alert.State != "resolving" || !alert.RecoveryStartedAt.Equal(now.Add(7*time.Minute)) {
		t.Fatalf("recovery observation=%+v", alert)
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(13*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "host:host-1")
	if err != nil {
		t.Fatal(err)
	}
	if alert.State != "resolved" || !alert.ResolvedAt.Equal(now.Add(13*time.Minute)) {
		t.Fatalf("resolved alert=%+v", alert)
	}
}

func TestFleetAlertDoesNotResolveWhileListenerTelemetryIsMissing(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "runnerhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	pool := core.RunnerPool{ID: "pool-1", Name: "private", GitHubInstallationID: 1}
	if err := st.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st,
		scaleSetStatuses: map[string]scaleSetStatusResponse{
			pool.ID: {PoolID: pool.ID, State: "backing_off", UpdatedAt: now},
		},
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err := st.GetFleetAlert(ctx, "listener:"+pool.ID)
	if err != nil || alert.State != "active" {
		t.Fatalf("alert=%+v err=%v", alert, err)
	}

	// A restart drops the in-memory status map; the missing observation is not recovery.
	svc.scaleSetStatuses = nil
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "listener:"+pool.ID)
	if err != nil || alert.State != "active" || !alert.RecoveryStartedAt.IsZero() {
		t.Fatalf("missing telemetry incorrectly advanced recovery: alert=%+v err=%v", alert, err)
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "listener:"+pool.ID)
	if err != nil || alert.State != "active" {
		t.Fatalf("unknown listener telemetry resolved an incident: alert=%+v err=%v", alert, err)
	}

	// A fresh healthy observation starts a new, sustained recovery window.
	svc.scaleSetStatuses = map[string]scaleSetStatusResponse{
		pool.ID: {PoolID: pool.ID, State: "ready", UpdatedAt: now.Add(21 * time.Minute)},
	}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(21*time.Minute)); err != nil {
		t.Fatal(err)
	}
	alert, err = st.GetFleetAlert(ctx, "listener:"+pool.ID)
	if err != nil || alert.State != "resolving" || !alert.RecoveryStartedAt.Equal(now.Add(21*time.Minute)) {
		t.Fatalf("fresh health did not start recovery: alert=%+v err=%v", alert, err)
	}
}

func TestServerFleetAlertsIgnoreHealthyIdleFleetAndMaintenanceListenerErrors(t *testing.T) {
	now := time.Now().UTC()
	if got := detectServerFleetAlerts(nil, nil, nil, nil, false, now); len(got) != 0 {
		t.Fatalf("empty idle fleet created alerts: %+v", got)
	}
	pools := []core.RunnerPool{{ID: "pool-1", Name: "private", GitHubInstallationID: 1}}
	readyStatus := []scaleSetStatusResponse{{PoolID: "pool-1", State: "ready", UpdatedAt: now}}
	if got := detectServerFleetAlerts(nil, pools, nil, readyStatus, false, now); len(got) != 0 {
		t.Fatalf("healthy idle connected pool created alerts: %+v", got)
	}
	staleStatus := []scaleSetStatusResponse{{PoolID: "pool-1", State: "ready", UpdatedAt: now.Add(-3 * time.Minute)}}
	if got := detectServerFleetAlerts(nil, pools, nil, staleStatus, false, now); len(got) == 0 {
		t.Fatal("stale listener heartbeat did not create alert candidates")
	}
	statuses := []scaleSetStatusResponse{{PoolID: "pool-1", State: "backing_off", UpdatedAt: now}}
	if got := detectServerFleetAlerts(nil, pools, nil, statuses, true, now); len(got) != 0 {
		t.Fatalf("intentional maintenance created listener alert: %+v", got)
	}
}

func TestFleetAlertAcknowledgeIsDurable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runnerhub.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	alert := store.FleetAlert{ID: "runtime:pool-1", State: "active", FirstSeenAt: time.Now().UTC()}
	if err := st.UpsertFleetAlert(ctx, alert); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{"admin-key": {Name: "admin", Role: auth.RoleAdmin}}})}
	denied := httptest.NewRecorder()
	svc.Handler().ServeHTTP(denied, httptest.NewRequest("POST", "/api/v1/fleet-alerts/runtime:pool-1/acknowledge", nil))
	if denied.Code != 401 {
		t.Fatalf("unauthorized acknowledge status=%d", denied.Code)
	}
	req := httptest.NewRequest("POST", "/api/v1/fleet-alerts/runtime:pool-1/acknowledge", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatalf("acknowledge status=%d body=%s", res.Code, res.Body.String())
	}
	alert, err = st.GetFleetAlert(ctx, alert.ID)
	if err != nil || alert.AcknowledgedAt.IsZero() {
		t.Fatalf("acknowledgement not persisted: alert=%+v err=%v", alert, err)
	}
	mute := httptest.NewRequest("POST", "/api/v1/fleet-alerts/runtime:pool-1/mute", nil)
	mute.Header.Set("Authorization", "Bearer admin-key")
	muteResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(muteResponse, mute)
	if muteResponse.Code != 200 {
		t.Fatalf("mute status=%d body=%s", muteResponse.Code, muteResponse.Body.String())
	}
	alert, err = st.GetFleetAlert(ctx, alert.ID)
	if err != nil || alert.MutedUntil.IsZero() {
		t.Fatalf("mute not persisted: alert=%+v err=%v", alert, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	alert, err = st.GetFleetAlert(ctx, alert.ID)
	if err != nil || alert.AcknowledgedAt.IsZero() || alert.MutedUntil.IsZero() {
		t.Fatalf("alert actions did not survive restart: alert=%+v err=%v", alert, err)
	}
}

func TestFleetNoReadyRecoveryFindsHealthyListenerAfterUnknownPool(t *testing.T) {
	now := time.Now().UTC()
	alert := store.FleetAlert{Cause: "no_ready_listeners"}
	pools := []core.RunnerPool{
		{ID: "stale-first", GitHubInstallationID: 1},
		{ID: "healthy-later", GitHubInstallationID: 1},
	}
	statuses := []scaleSetStatusResponse{
		{PoolID: "stale-first", State: "ready", UpdatedAt: now.Add(-3 * time.Minute)},
		{PoolID: "healthy-later", State: "ready", UpdatedAt: now},
	}
	if listenerAlertSignalUnknown(alert, pools, statuses, false, now) {
		t.Fatal("a stale earlier pool masked a later fresh healthy listener")
	}

	if !listenerAlertSignalUnknown(alert, pools[:1], statuses[:1], false, now) {
		t.Fatal("missing fleet-wide recovery evidence should remain unknown")
	}
}

func TestFleetAlertsDetectSustainedCleanupBacklogAndSuppressMaintenance(t *testing.T) {
	now := time.Now().UTC()
	workers := []core.Worker{
		{ID: "w1", PoolID: "pool-1", State: core.WorkerCleanupPending, UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "w2", PoolID: "pool-1", State: core.WorkerCleanupPending, UpdatedAt: now.Add(-7 * time.Minute)},
		{ID: "w3", PoolID: "pool-1", State: core.WorkerCleanupPending, UpdatedAt: now.Add(-8 * time.Minute)},
	}
	findCleanup := func(candidates []fleetAlertCandidate) bool {
		for _, candidate := range candidates {
			if candidate.Cause == "worker_cleanup_backlog" && candidate.EntityID == "pool-1" {
				return true
			}
		}
		return false
	}
	if !findCleanup(detectServerFleetAlerts(nil, nil, workers, nil, false, now)) {
		t.Fatal("three stale cleanup reservations should create a backlog candidate")
	}
	if findCleanup(detectServerFleetAlerts(nil, nil, workers[:2], nil, false, now)) {
		t.Fatal("fewer than three stale cleanup reservations must not alert")
	}
	fresh := append([]core.Worker(nil), workers...)
	fresh[2].UpdatedAt = now.Add(-time.Minute)
	if findCleanup(detectServerFleetAlerts(nil, nil, fresh, nil, false, now)) {
		t.Fatal("fresh cleanup progress must not count toward a stale backlog")
	}
	if got := detectServerFleetAlerts(nil, nil, workers, nil, true, now); len(got) != 0 {
		t.Fatalf("maintenance should suppress fleet alerts: %+v", got)
	}
}

func TestFleetAlertConcurrentActionsSurviveSampling(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.UpsertHost(ctx, core.Host{ID: "offline", State: core.HostOffline, LastSeenAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{"admin-key": {Name: "admin", Role: auth.RoleAdmin}}})}
	if err := svc.reconcileFleetAlertsAt(ctx, now.Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	handler := svc.Handler()
	// Each round races two independent operator actions with the sampler.
	// All must preserve both actions and the monitor's active incident state.
	for round := 0; round < 30; round++ {
		alert, err := st.GetFleetAlert(ctx, "host:offline")
		if err != nil {
			t.Fatal(err)
		}
		alert.AcknowledgedAt = time.Time{}
		alert.MutedUntil = time.Time{}
		if err := st.UpsertFleetAlert(ctx, alert); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, action := range []string{"acknowledge", "mute", "sample"} {
			wg.Add(1)
			go func(action string) {
				defer wg.Done()
				<-start
				if action == "sample" {
					if err := svc.reconcileFleetAlertsAt(ctx, now); err != nil {
						t.Error(err)
					}
					return
				}
				req := httptest.NewRequest("POST", "/api/v1/fleet-alerts/host:offline/"+action, nil)
				req.Header.Set("Authorization", "Bearer admin-key")
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code != 200 {
					t.Errorf("%s: status %d: %s", action, res.Code, res.Body.String())
				}
			}(action)
		}
		close(start)
		wg.Wait()
		alert, err = st.GetFleetAlert(ctx, "host:offline")
		if err != nil || alert.AcknowledgedAt.IsZero() || alert.MutedUntil.IsZero() || alert.State != "active" {
			t.Fatalf("lost concurrent update: %+v err=%v", alert, err)
		}
	}
}
