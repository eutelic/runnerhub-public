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

func TestArchivingOldEnrollmentRevokesAgentAuthentication(t *testing.T) {
  ctx:=context.Background()
  st,err:=store.OpenSQLite(":memory:")
  if err!=nil {t.Fatal(err)}
  defer st.Close()
  hub,err:=New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
  if err!=nil {t.Fatal(err)}
  caps:=provider.Capabilities{Provider:"docker",OS:"linux",Architecture:"amd64"}
  enrollment,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{Name:"compute",Provider:"docker",Capabilities:caps})
  if err!=nil {t.Fatal(err)}
  if !hub.Authenticate(enrollment.HostID,enrollment.AgentToken) {t.Fatal("newly enrolled agent should authenticate")}
  host,err:=st.GetHost(ctx,enrollment.HostID)
  if err!=nil {t.Fatal(err)}
  host.State=core.HostOffline
  host.LastSeenAt=time.Now().Add(-24*time.Hour)
  if err:=st.UpsertHost(ctx,host);err!=nil {t.Fatal(err)}
  if _,err:=st.ArchiveHost(ctx,enrollment.HostID,"",time.Now());err!=nil {t.Fatal(err)}
  if hub.Authenticate(enrollment.HostID,enrollment.AgentToken) {t.Fatal("archived host token still authenticates")}
  if err:=hub.Heartbeat(ctx,agent.Heartbeat{HostID:enrollment.HostID,Capabilities:caps});err==nil {
    t.Fatal("archived host heartbeat unexpectedly succeeded")
  }
}
