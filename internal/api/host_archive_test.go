package api

import (
  "bytes"
  "context"
  "encoding/json"
  "errors"
  "net/http"
  "net/http/httptest"
  "testing"
  "time"

  "github.com/eutelic/runnerhub/internal/agent"
  "github.com/eutelic/runnerhub/internal/auth"
  "github.com/eutelic/runnerhub/internal/core"
  "github.com/eutelic/runnerhub/internal/store"
)

func setupHostArchiveTest(t *testing.T) (*store.SQLite, *Service, time.Time) {
  t.Helper()
  st, err := store.OpenSQLite(":memory:")
  if err != nil { t.Fatal(err) }
  t.Cleanup(func(){ _ = st.Close() })
  now := time.Now().UTC()
  hosts := []core.Host{
    {ID:"host-old",Name:"compute",Provider:"docker",State:core.HostOffline,LastSeenAt:now.Add(-48*time.Hour)},
    {ID:"host-live",Name:"compute",Provider:"docker",State:core.HostOnline,LastSeenAt:now},
  }
  for _,h := range hosts { if err := st.UpsertHost(context.Background(), h); err != nil { t.Fatal(err) } }
  svc := &Service{Store:st,Auth:auth.New(auth.Config{APIKeys:map[string]auth.Principal{
    "admin-secret":{Name:"test-admin",Role:auth.RoleAdmin},
    "operator-secret":{Name:"test-operator",Role:auth.RoleOperator},
  }})}
  return st,svc,now
}
func hostArchiveRequest(svc *Service, id, target, key string) *httptest.ResponseRecorder {
  b,_:=json.Marshal(map[string]string{"targetHostId":target})
  req:=httptest.NewRequest(http.MethodPost,"/api/v1/hosts/"+id+"/archive",bytes.NewReader(b))
  req.Header.Set("Authorization","Bearer "+key)
  req.Header.Set("Content-Type","application/json")
  rr:=httptest.NewRecorder()
  svc.Handler().ServeHTTP(rr,req)
  return rr
}

func TestMergeOldHostPreservesHistoricalIDsAndRevokesWrites(t *testing.T) {
  st,svc,now := setupHostArchiveTest(t)
  ctx:=context.Background()
  worker:=core.Worker{ID:"worker-old",HostID:"host-old",State:core.WorkerDeleted}
  if err:=st.UpsertWorker(ctx,worker);err!=nil{t.Fatal(err)}

  if rr:=hostArchiveRequest(svc,"host-old","host-live","operator-secret");rr.Code!=http.StatusForbidden {
    t.Fatalf("operator archive status=%d body=%s",rr.Code,rr.Body.String())
  }
  rr:=hostArchiveRequest(svc,"host-old","host-live","admin-secret")
  if rr.Code!=http.StatusOK{t.Fatalf("archive status=%d body=%s",rr.Code,rr.Body.String())}
  old,err:=st.GetHost(ctx,"host-old")
  if err!=nil{t.Fatal(err)}
  if old.State!=core.HostArchived||old.MergedIntoHostID!="host-live"||old.ArchivedAt.Before(now) {
    t.Fatalf("old host not archived/merged correctly: %+v",old)
  }
  live,err:=st.GetHost(ctx,"host-live")
  if err!=nil{t.Fatal(err)}
  if live.State!=core.HostOnline { t.Fatalf("target host should remain online: %+v",live) }
  historical,err:=st.GetWorker(ctx,worker.ID)
  if err!=nil{t.Fatal(err)}
  if historical.HostID!="host-old" { t.Fatalf("historical worker host identity rewritten: %q",historical.HostID) }
  old.State=core.HostOnline
  if err:=st.UpsertHost(ctx,old);!errors.Is(err,store.ErrHostArchived) {
    t.Fatalf("expected archived identity to reject updates, got %v",err)
  }
  rr=hostArchiveRequest(svc,"host-old","host-live","admin-secret")
  if rr.Code!=http.StatusOK { t.Fatalf("same-target retry should be idempotent: %d %s",rr.Code,rr.Body.String()) }
}

func TestArchiveWithoutMergeDoesNotRequireTarget(t *testing.T) {
  st,svc,_:=setupHostArchiveTest(t)
  rr:=hostArchiveRequest(svc,"host-old","","admin-secret")
  if rr.Code!=http.StatusOK { t.Fatalf("archive status=%d body=%s",rr.Code,rr.Body.String()) }
  old,err:=st.GetHost(context.Background(),"host-old")
  if err!=nil { t.Fatal(err) }
  if old.MergedIntoHostID!="" || old.State!=core.HostArchived { t.Fatalf("unexpected retired record: %+v",old) }
}

func TestMergeHostRefusesUnsafeTargetsAndUnfinishedJobs(t *testing.T) {
  tests:=[]struct{name string;prepare func(*testing.T,*store.SQLite,time.Time);target string}{
    {"online source",func(t *testing.T,s *store.SQLite,_ time.Time){h,_:=s.GetHost(context.Background(),"host-old");h.State=core.HostOnline;if err:=s.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}},"host-live"},
    {"unknown destination",nil,"host-missing"},
    {"self destination",nil,"host-old"},
    {"busy old worker",func(t *testing.T,s *store.SQLite,_ time.Time){if err:=s.UpsertWorker(context.Background(),core.Worker{ID:"worker-1",HostID:"host-old",State:core.WorkerBusy});err!=nil{t.Fatal(err)}},"host-live"},
    {"unfinished cleanup",func(t *testing.T,s *store.SQLite,_ time.Time){if err:=s.UpsertWorker(context.Background(),core.Worker{ID:"worker-1",HostID:"host-old",State:core.WorkerCleanupPending});err!=nil{t.Fatal(err)}},"host-live"},
    {"unacked command",func(t *testing.T,s *store.SQLite,now time.Time){if err:=s.EnqueueCommand(context.Background(),"host-old",agent.Command{ID:"command-1",WorkerID:"worker-1",Type:"inspect",CreatedAt:now});err!=nil{t.Fatal(err)}},"host-live"},
    {"wrong provider",func(t *testing.T,s *store.SQLite,_ time.Time){h,_:=s.GetHost(context.Background(),"host-live");h.Provider="process";if err:=s.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}},"host-live"},
    {"wrong name",func(t *testing.T,s *store.SQLite,_ time.Time){h,_:=s.GetHost(context.Background(),"host-live");h.Name="other";if err:=s.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}},"host-live"},
    {"stale destination",func(t *testing.T,s *store.SQLite,now time.Time){h,_:=s.GetHost(context.Background(),"host-live");h.LastSeenAt=now.Add(-10*time.Minute);if err:=s.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}},"host-live"},
  }
  for _,tt:=range tests {t.Run(tt.name,func(t *testing.T){
    st,svc,now:=setupHostArchiveTest(t)
    if tt.prepare!=nil { tt.prepare(t,st,now) }
    rr:=hostArchiveRequest(svc,"host-old",tt.target,"admin-secret")
    if rr.Code!=http.StatusConflict {t.Fatalf("unsafe request status=%d body=%s",rr.Code,rr.Body.String())}
    old,err:=st.GetHost(context.Background(),"host-old")
    if err!=nil{t.Fatal(err)}
    if old.State==core.HostArchived {t.Fatal("unsafe archive changed source host")}
  })}
}
