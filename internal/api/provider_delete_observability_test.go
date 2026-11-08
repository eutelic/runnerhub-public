package api

import (
    "context"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/eutelic/runnerhub/internal/agent"
    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/store"
    "github.com/eutelic/runnerhub/internal/telemetry"
)

func TestProviderDeleteResultRecordsAgentEvidenceWithoutClaimingDiskReclamation(t *testing.T) {
    ctx := context.Background()
    st, err := store.OpenSQLite(":memory:")
    if err != nil { t.Fatal(err) }
    defer st.Close()
    metrics := &telemetry.Metrics{}
    svc := &Service{Store:st, Metrics:metrics}
    hostID := "host-cleanup"

    for _, tc := range []struct {
        id string
        ok bool
        duration int64
    }{
        {id:"success", ok:true, duration:int64(250*time.Millisecond)},
        {id:"failure", ok:false, duration:0}, // Older agents omit duration.
    } {
        workerID := "worker-" + tc.id
        commandID := "delete-" + tc.id
        if err := st.UpsertWorker(ctx, core.Worker{
            ID:workerID, HostID:hostID, State:core.WorkerCleanupPending,
        }); err != nil { t.Fatal(err) }
        if err := st.EnqueueCommand(ctx, hostID, agent.Command{
            ID:commandID, WorkerID:workerID, Type:agent.CommandDelete,
        }); err != nil { t.Fatal(err) }
        if err := svc.HandleAgentResult(ctx, hostID, agent.CommandResult{
            CommandID:commandID, WorkerID:workerID, OK:tc.ok,
            ProviderDeleteDurationNanos:tc.duration,
        }); err != nil { t.Fatal(err) }
    }

    snapshot := metrics.Snapshot()
    if snapshot.ProviderDeleteAttempts != 2 || snapshot.ProviderDeleteFailures != 1 ||
        snapshot.ProviderDeleteMeasured != 1 ||
        snapshot.ProviderDeleteDurationTotal != 250*time.Millisecond {
        t.Fatalf("incorrect process-local delete metrics: %+v",snapshot)
    }
    if snapshot.Scope != "process-local-volatile" {
        t.Fatalf("missing metric reset scope: %+v",snapshot)
    }

    events, err := st.ListAudit(ctx, 20)
    if err != nil { t.Fatal(err) }
    found := map[string]core.AuditEvent{}
    for _, event := range events {
        if event.Action == "provider_delete_result" { found[event.ResourceID] = event }
    }
    if len(found) != 2 { t.Fatalf("expected two durable provider delete events, got %+v",found) }
    if found["worker-success"].Result != "agent_reported_success" ||
        !strings.Contains(found["worker-success"].SafeDetail, "agent_duration_ns=") {
        t.Fatalf("successful provider acknowledgement missing: %+v",found["worker-success"])
    }
    if found["worker-failure"].Result != "agent_reported_failure" ||
        !strings.Contains(found["worker-failure"].SafeDetail, "duration unavailable") {
        t.Fatalf("failed provider acknowledgement missing: %+v",found["worker-failure"])
    }
}

func TestUnrelatedAgentResultDoesNotInflateProviderDeletion(t *testing.T) {
    ctx := context.Background()
    st, err := store.OpenSQLite(":memory:")
    if err != nil { t.Fatal(err) }
    defer st.Close()
    if err := st.UpsertWorker(ctx,core.Worker{ID:"inspect-worker",HostID:"host-cleanup",State:core.WorkerReady}); err != nil { t.Fatal(err) }
    if err := st.EnqueueCommand(ctx,"host-cleanup",agent.Command{
        ID:"inspect-command",WorkerID:"inspect-worker",Type:agent.CommandInspect,
    }); err != nil { t.Fatal(err) }
    metrics := &telemetry.Metrics{}
    svc := &Service{Store:st,Metrics:metrics}
    if err := svc.HandleAgentResult(ctx,"host-cleanup",agent.CommandResult{
        CommandID:"inspect-command",WorkerID:"inspect-worker",OK:true,
        ProviderDeleteDurationNanos:int64(time.Second),
    }); err != nil { t.Fatal(err) }
    if metrics.Snapshot().ProviderDeleteAttempts != 0 {
        t.Fatal("inspection result was counted as a provider deletion")
    }
}

func TestProviderDeleteReplayDoesNotDuplicateAudit(t *testing.T) {
    ctx := context.Background()
    dbPath := filepath.Join(t.TempDir(), "runnerhub.db")
    st, err := store.OpenSQLite(dbPath)
    if err != nil { t.Fatal(err) }
    hostID, workerID, commandID := "host-replay", "worker-replay", "delete-replay"
    if err := st.UpsertWorker(ctx, core.Worker{ID:workerID, HostID:hostID, State:core.WorkerCleanupPending}); err != nil { t.Fatal(err) }
    if err := st.EnqueueCommand(ctx,hostID,agent.Command{ID:commandID,WorkerID:workerID,Type:agent.CommandDelete}); err != nil { t.Fatal(err) }
    metrics := &telemetry.Metrics{}
    svc := &Service{Store:st,Metrics:metrics}
    result := agent.CommandResult{CommandID:commandID,WorkerID:workerID,OK:true,ProviderDeleteDurationNanos:int64(5*time.Millisecond)}
    // Simulate a successful callback whose command ACK was lost.
    for i:=0;i<2;i++ {
        if err:=svc.HandleAgentResult(ctx,hostID,result);err!=nil{t.Fatal(err)}
    }
    first:=metrics.Snapshot()
    if first.ProviderDeleteAttempts!=1||first.ProviderDeleteMeasured!=1||first.ProviderDeleteDurationTotal!=5*time.Millisecond{
        t.Fatalf("replayed callback inflated process-local metrics: %+v",first)
    }
    if err:=st.Close();err!=nil{t.Fatal(err)}

    // Durable command-ID fencing must survive a controller restart.
    reopened,err:=store.OpenSQLite(dbPath)
    if err!=nil{t.Fatal(err)}
    defer reopened.Close()
    afterRestart:=&telemetry.Metrics{}
    freshSvc:=&Service{Store:reopened,Metrics:afterRestart}
    if err:=freshSvc.HandleAgentResult(ctx,hostID,result);err!=nil{t.Fatal(err)}
    if afterRestart.Snapshot().ProviderDeleteAttempts!=0{t.Fatal("replay after restart was counted again")}
    events,err:=reopened.ListAudit(ctx,50)
    if err!=nil{t.Fatal(err)}
    found:=0
    for _,event:=range events{
        if event.Action=="provider_delete_result"&&event.ResourceID==workerID{found++}
    }
    if found!=1{t.Fatalf("want one durable audit event for delete command, got %d",found)}
}

func TestProviderDeleteResultRejectsMismatchedCommandIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const (
		hostID = "host-owner"
		workerID = "worker-owner"
		commandID = "delete-owner"
	)
	if err := st.UpsertWorker(ctx, core.Worker{
		ID: workerID, HostID: hostID, State: core.WorkerCleanupPending,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueCommand(ctx, hostID, agent.Command{
		ID: commandID, WorkerID: workerID, Type: agent.CommandDelete,
	}); err != nil {
		t.Fatal(err)
	}

	metrics := &telemetry.Metrics{}
	svc := &Service{Store: st, Metrics: metrics}
	for _, tc := range []struct {
		name string
		host string
		command string
		worker string
	}{
		{name: "wrong host", host: "host-stranger", command: commandID, worker: workerID},
		{name: "wrong worker", host: hostID, command: commandID, worker: "worker-stranger"},
		{name: "unknown command", host: hostID, command: "delete-unknown", worker: workerID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.recordProviderDeleteResult(ctx, tc.host, agent.CommandResult{
				CommandID: tc.command, WorkerID: tc.worker, OK: true,
				ProviderDeleteDurationNanos: int64(time.Second),
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	events, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == "provider_delete_result" {
			t.Fatalf("mismatched command identity inserted an audit event: %+v", event)
		}
	}
	if got := metrics.Snapshot().ProviderDeleteAttempts; got != 0 {
		t.Fatalf("mismatched command identities inflated attempts: %d", got)
	}

	if err := svc.recordProviderDeleteResult(ctx, hostID, agent.CommandResult{
		CommandID: commandID, WorkerID: workerID, OK: true,
		ProviderDeleteDurationNanos: int64(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if got := metrics.Snapshot().ProviderDeleteAttempts; got != 1 {
		t.Fatalf("valid identity should be recorded once, got %d", got)
	}
}

func TestProviderDeleteInvalidDurationIsNotMeasured(t *testing.T) {
 for _, tc := range []struct{name string; duration int64}{
  {"negative", -1},
  {"over_24_hours", int64(24*time.Hour)+1},
 } {
  t.Run(tc.name,func(t *testing.T){
   ctx:=context.Background()
   st,err:=store.OpenSQLite(":memory:")
   if err!=nil{t.Fatal(err)}
   defer st.Close()
   const hostID="host-duration"
   const workerID="worker-duration"
   const commandID="delete-duration"
   if err:=st.UpsertWorker(ctx,core.Worker{ID:workerID,HostID:hostID,State:core.WorkerCleanupPending});err!=nil{t.Fatal(err)}
   if err:=st.EnqueueCommand(ctx,hostID,agent.Command{ID:commandID,WorkerID:workerID,Type:agent.CommandDelete});err!=nil{t.Fatal(err)}
   metrics:=&telemetry.Metrics{}
   svc:=&Service{Store:st,Metrics:metrics}
   if err:=svc.recordProviderDeleteResult(ctx,hostID,agent.CommandResult{CommandID:commandID,WorkerID:workerID,OK:true,ProviderDeleteDurationNanos:tc.duration});err!=nil{t.Fatal(err)}
   snapshot:=metrics.Snapshot()
   if snapshot.ProviderDeleteAttempts!=1||snapshot.ProviderDeleteMeasured!=0||snapshot.ProviderDeleteDurationTotal!=0{
    t.Fatalf("invalid duration must count attempt but not measurement: %+v",snapshot)
   }
   events,err:=st.ListAudit(ctx,10)
   if err!=nil{t.Fatal(err)}
   found:=false
   for _,event:=range events{
    if event.Action=="provider_delete_result"{
     found=true
     if !strings.Contains(event.SafeDetail,"duration unavailable"){t.Fatalf("invalid duration recorded as valid: %+v",event)}
    }
   }
   if !found{t.Fatal("missing provider deletion audit")}
  })
 }
}
