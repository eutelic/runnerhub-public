package api

import(
"bytes";"context";"encoding/json";"errors";"net/http";"net/http/httptest";"testing";"time";"github.com/eutelic/runnerhub/internal/agent";"github.com/eutelic/runnerhub/internal/agenthub";"github.com/eutelic/runnerhub/internal/auth";cachepkg "github.com/eutelic/runnerhub/internal/cache";"github.com/eutelic/runnerhub/internal/core";"github.com/eutelic/runnerhub/internal/fleet";"github.com/eutelic/runnerhub/internal/githubrunner";"github.com/eutelic/runnerhub/internal/policy";"github.com/eutelic/runnerhub/internal/provider";"github.com/eutelic/runnerhub/internal/scheduler";"github.com/eutelic/runnerhub/internal/store";"github.com/eutelic/runnerhub/internal/telemetry")
type failingCommandStore struct{store.Store;fail bool}
func(s *failingCommandStore)EnqueueCommand(ctx context.Context,hostID string,c agent.Command)error{if s.fail{return errors.New("simulated command persistence failure")};return s.Store.EnqueueCommand(ctx,hostID,c)}
type apiFakeGH struct{deleted []int64}
func(g *apiFakeGH)GenerateJITConfig(_ context.Context,r githubrunner.JITConfigRequest)(githubrunner.JITConfig,error){return githubrunner.JITConfig{RunnerID:77,RunnerName:r.RunnerName,EncodedConfig:"jit-secret"},nil}
func(g *apiFakeGH)GetRunner(context.Context,core.GitHubScope,int64)(githubrunner.Runner,error){return githubrunner.Runner{},githubrunner.ErrNotFound}
func(g *apiFakeGH)DeleteRunner(_ context.Context,_ core.GitHubScope,id int64)error{g.deleted=append(g.deleted,id);return nil}
type apiFakeCatalog struct{}
func(apiFakeCatalog)ListRepositories(context.Context,core.GitHubScope)([]githubrunner.Repository,error){return []githubrunner.Repository{{ID:1,FullName:"acme/app",Private:true}},nil}
func(apiFakeCatalog)GetRepository(context.Context,string,string)(githubrunner.Repository,error){return githubrunner.Repository{ID:1,FullName:"acme/app",Private:true},nil}
func(apiFakeCatalog)ListRunnerGroups(context.Context,string)([]githubrunner.RunnerGroup,error){return nil,nil}
func(apiFakeCatalog)GetRunnerGroup(context.Context,string,int64)(githubrunner.RunnerGroup,error){return githubrunner.RunnerGroup{},nil}
func(apiFakeCatalog)ListRunnerGroupRepositories(context.Context,string,int64)([]githubrunner.Repository,error){return nil,nil}
func TestCreateWorkerCleansGitHubRunnerWhenCommandPersistenceFails(t *testing.T){ctx:=context.Background();raw,err:=store.OpenSQLite(":memory:");if err!=nil{t.Fatal(err)};defer raw.Close();st:=&failingCommandStore{Store:raw};hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef");if err!=nil{t.Fatal(err)};enrolled,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{Name:"host",Provider:"docker",Capabilities:provider.Capabilities{Provider:"docker",Capacity:core.Resources{CPUMillis:4000,MemoryBytes:8<<30},IsolationModes:[]core.IsolationMode{core.IsolationContainerTrusted},Labels:map[string]string{"isolation.container-trusted":"true"}}});if err!=nil{t.Fatal(err)};if enrolled.HostID==""{t.Fatal("missing host id")};pool:=core.RunnerPool{ID:"pool",Name:"secure",Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"acme",Repository:"app"},Labels:[]string{"secure"},Isolation:core.IsolationContainerTrusted,Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30},MaxConcurrency:1,RunnerGroupID:1,Image:"img"};if err:=st.UpsertPool(ctx,pool);err!=nil{t.Fatal(err)};gh:=&apiFakeGH{};svc:=&Service{Store:st,GitHub:gh,Catalog:apiFakeCatalog{},Hub:hub,Scheduler:scheduler.Scheduler{Store:st},Policy:policy.Engine{AllowedImages:map[string]struct{}{"img":{}}}};st.fail=true;req:=httptest.NewRequest(http.MethodPost,"/api/v1/pools/pool/workers",bytes.NewBufferString(`{"repository":"acme/app"}`));req.Header.Set("Content-Type","application/json");rr:=httptest.NewRecorder();svc.Handler().ServeHTTP(rr,req);if rr.Code!=http.StatusInternalServerError{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())};if len(gh.deleted)!=1||gh.deleted[0]!=77{t.Fatalf("GitHub cleanup=%v",gh.deleted)};workers,err:=st.ListWorkers(ctx);if err!=nil{t.Fatal(err)};if len(workers)!=1||workers[0].State!=core.WorkerFailed{t.Fatalf("worker state=%+v",workers)}}
func TestCacheOversizeReturns413(t *testing.T){cache:=&cachepkg.Store{Root:t.TempDir(),MaxBytes:4};svc:=&Service{Cache:cache};req:=httptest.NewRequest(http.MethodPut,"/api/v1/cache",bytes.NewBufferString("hello"));rr:=httptest.NewRecorder();svc.Handler().ServeHTTP(rr,req);if rr.Code!=http.StatusRequestEntityTooLarge{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}}
func TestUnexpectedProviderDeletionMarksWorkerFailed(t *testing.T){ctx:=context.Background();st,_:=store.OpenSQLite(":memory:");defer st.Close();wk:=core.Worker{ID:"w",State:core.WorkerReady,UpdatedAt:time.Now().UTC()};_ = st.UpsertWorker(ctx,wk);svc:=&Service{Store:st};state:=provider.WorkerState{State:core.WorkerDeleted};if err:=svc.HandleAgentResult(ctx,"host",agent.CommandResult{WorkerID:"w",OK:true,State:&state});err!=nil{t.Fatal(err)};got,_:=st.GetWorker(ctx,"w");if got.State!=core.WorkerFailed||got.FailureReason==""{t.Fatalf("unexpected state=%+v",got)}}

func TestScaleSetStatusRoundTrip(t *testing.T){ctx:=context.Background();st,err:=store.OpenSQLite(":memory:");if err!=nil{t.Fatal(err)};defer st.Close();pool:=core.RunnerPool{ID:"pool-status",Name:"status",Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"acme",Repository:"app"},Labels:[]string{"runnerhub"},Isolation:core.IsolationContainerTrusted,Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30},MaxConcurrency:1,RunnerGroupID:1,GitHubInstallationID:123,ScaleSetName:"runnerhub",Image:"img"};if err:=st.UpsertPool(ctx,pool);err!=nil{t.Fatal(err)};svc:=&Service{Store:st};req:=httptest.NewRequest(http.MethodPost,"/api/v1/scalesets/status",bytes.NewBufferString(`{"poolId":"pool-status","state":"ready"}`));req.Header.Set("Content-Type","application/json");rr:=httptest.NewRecorder();svc.Handler().ServeHTTP(rr,req);if rr.Code!=http.StatusOK{t.Fatalf("post status=%d body=%s",rr.Code,rr.Body.String())};req=httptest.NewRequest(http.MethodGet,"/api/v1/scalesets/status",nil);rr=httptest.NewRecorder();svc.Handler().ServeHTTP(rr,req);if rr.Code!=http.StatusOK{t.Fatalf("get status=%d body=%s",rr.Code,rr.Body.String())};var got []scaleSetStatusResponse;if err:=json.NewDecoder(rr.Body).Decode(&got);err!=nil{t.Fatal(err)};if len(got)!=1||got[0].PoolID!="pool-status"||got[0].State!="ready"{t.Fatalf("statuses=%+v",got)}}
func TestScaleSetStatusRejectsUnknownPool(t *testing.T){st,err:=store.OpenSQLite(":memory:");if err!=nil{t.Fatal(err)};defer st.Close();svc:=&Service{Store:st};req:=httptest.NewRequest(http.MethodPost,"/api/v1/scalesets/status",bytes.NewBufferString(`{"poolId":"missing","state":"error","detail":"boom"}`));req.Header.Set("Content-Type","application/json");rr:=httptest.NewRecorder();svc.Handler().ServeHTTP(rr,req);if rr.Code!=http.StatusNotFound{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}}


func TestNormalizeConnectResourcesDefaultsAndOverrides(t *testing.T){
	got:=normalizeConnectResources(core.Resources{},core.Resources{})
	if got.CPUMillis!=2500||got.MemoryBytes!=4<<30||got.DiskBytes!=20<<30{t.Fatalf("defaults=%+v",got)}
	fallback:=core.Resources{CPUMillis:1000,MemoryBytes:2<<30,DiskBytes:10<<30}
	got=normalizeConnectResources(core.Resources{CPUMillis:3000,MemoryBytes:6<<30},fallback)
	if got.CPUMillis!=3000||got.MemoryBytes!=6<<30||got.DiskBytes!=10<<30{t.Fatalf("override=%+v",got)}
}


func TestUpdatePoolResources(t *testing.T){
	ctx:=context.Background()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()
	pool:=core.RunnerPool{ID:"pool-resources",Name:"resources",Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"acme",Repository:"app"},Labels:[]string{"runnerhub"},Isolation:core.IsolationContainerTrusted,Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30,DiskBytes:20<<30},MaxConcurrency:1,RunnerGroupID:1}
	if err:=st.UpsertPool(ctx,pool);err!=nil{t.Fatal(err)}
	svc:=&Service{Store:st}
	req:=httptest.NewRequest(http.MethodPatch,"/api/v1/pools/pool-resources/resources",bytes.NewBufferString(`{"resources":{"cpuMillis":2500,"memoryBytes":4294967296},"maxConcurrency":2}`))
	req.Header.Set("Content-Type","application/json")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}
	got,err:=st.GetPool(ctx,"pool-resources")
	if err!=nil{t.Fatal(err)}
	if got.Resources.CPUMillis!=2500||got.Resources.MemoryBytes!=4<<30||got.Resources.DiskBytes!=20<<30||got.MaxConcurrency!=2{t.Fatalf("pool=%+v",got)}
}

func TestRuntimeHealthEndpointSeparatesRuntimeEvidenceFromListenerState(t *testing.T){
	ctx:=context.Background()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()
	now:=time.Now().UTC()
	pool:=core.RunnerPool{ID:"pool-runtime",Name:"runtime",Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"acme",Repository:"app"},Labels:[]string{"runnerhub"},Isolation:core.IsolationContainerTrusted,Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30},MaxConcurrency:1,RunnerGroupID:1}
	if err:=st.UpsertPool(ctx,pool);err!=nil{t.Fatal(err)}
	if err:=st.UpsertWorker(ctx,core.Worker{ID:"done",PoolID:pool.ID,State:core.WorkerDeleted,RegisteredAt:now.Add(-3*time.Minute),StartedAt:now.Add(-2*time.Minute),CompletedAt:now.Add(-time.Minute)});err!=nil{t.Fatal(err)}
	svc:=&Service{Store:st}
	svc.setScaleSetStatus(pool.ID,"ready","listener connected")
	req:=httptest.NewRequest(http.MethodGet,"/api/v1/runtime-health",nil)
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}
	var got []fleet.RuntimeHealth
	if err:=json.NewDecoder(rr.Body).Decode(&got);err!=nil{t.Fatal(err)}
	if len(got)!=1||got[0].PoolID!=pool.ID||got[0].State!="healthy"{t.Fatalf("runtime=%+v",got)}
}

func TestScalerWorkerViewOmitsProviderDiagnostics(t *testing.T){
	ctx:=context.Background()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()
	if err:=st.UpsertWorker(ctx,core.Worker{ID:"w-secret",State:core.WorkerFailed,ProviderDiagnostics:"redacted startup evidence"});err!=nil{t.Fatal(err)}
	authm:=auth.New(auth.Config{APIKeys:auth.ParseKeys("scale:scaler:listener,admin:admin:local")})
	svc:=&Service{Store:st,Auth:authm}

	req:=httptest.NewRequest(http.MethodGet,"/api/v1/workers",nil)
	req.Header.Set("Authorization","Bearer scale")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("scaler status=%d body=%s",rr.Code,rr.Body.String())}
	if bytes.Contains(rr.Body.Bytes(),[]byte("redacted startup evidence")){t.Fatalf("scaler response exposed diagnostics: %s",rr.Body.String())}

	req=httptest.NewRequest(http.MethodGet,"/api/v1/workers",nil)
	req.Header.Set("Authorization","Bearer admin")
	rr=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("admin status=%d body=%s",rr.Code,rr.Body.String())}
	if !bytes.Contains(rr.Body.Bytes(),[]byte("redacted startup evidence")){t.Fatalf("admin response omitted diagnostics: %s",rr.Body.String())}
}


func TestPublisherPoolClassification(t *testing.T){
	standard:=core.RunnerPool{ScaleSetName:"runnerhub",Labels:[]string{"runnerhub"}}
	if isPublisherPool(standard){t.Fatal("standard pool classified as publisher")}
	byName:=core.RunnerPool{ScaleSetName:publisherScaleSetName}
	if !isPublisherPool(byName){t.Fatal("publisher scale-set name not detected")}
	byLabel:=core.RunnerPool{Labels:[]string{publisherScaleSetName}}
	if !isPublisherPool(byLabel){t.Fatal("publisher label not detected")}
}

func TestPublisherMaxConcurrencyDefaultsToTwoAndPreservesExistingChoice(t *testing.T){
	if got:=publisherMaxConcurrency(0,nil);got!=2{t.Fatalf("new publisher default=%d, want 2",got)}
	existing:=core.RunnerPool{MaxConcurrency:1}
	if got:=publisherMaxConcurrency(0,&existing);got!=1{t.Fatalf("existing publisher max=%d, want preserved 1",got)}
	if got:=publisherMaxConcurrency(3,&existing);got!=3{t.Fatalf("requested publisher max=%d, want 3",got)}
}

func TestNormalizePublisherResourcesDefaultsAndOverrides(t *testing.T){
	got:=normalizePublisherResources(core.Resources{},core.Resources{})
	if got.CPUMillis!=2500||got.MemoryBytes!=8<<30||got.DiskBytes!=40<<30{t.Fatalf("defaults=%+v",got)}
	fallback:=core.Resources{CPUMillis:1500,MemoryBytes:6<<30,DiskBytes:30<<30}
	got=normalizePublisherResources(core.Resources{CPUMillis:3000,MemoryBytes:12<<30},fallback)
	if got.CPUMillis!=3000||got.MemoryBytes!=12<<30||got.DiskBytes!=30<<30{t.Fatalf("override=%+v",got)}
}


func TestScaleEventTreatsLateTerminalEventForDeletedWorkerAsSuccess(t *testing.T){
	ctx:=context.Background()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()
	now:=time.Now().UTC()
	wk:=core.Worker{ID:"w-late",PoolID:"pool-late",RunnerName:"runner-late",State:core.WorkerDeleted,UpdatedAt:now,CompletedAt:now.Add(-time.Second)}
	if err:=st.UpsertWorker(ctx,wk);err!=nil{t.Fatal(err)}
	svc:=&Service{Store:st}
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/scale-events",bytes.NewBufferString(`{"poolId":"pool-late","runnerName":"runner-late","state":"completed","result":"succeeded"}`))
	req.Header.Set("Content-Type","application/json")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}
	got,err:=st.GetWorker(ctx,"w-late")
	if err!=nil{t.Fatal(err)}
	if got.State!=core.WorkerDeleted{t.Fatalf("late completion resurrected worker: %+v",got)}
}


func TestPoolRecommendationsUsePoolSpecificQueueAndCapacity(t *testing.T){
	ctx:=context.Background()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()

	fast:=core.RunnerPool{ID:"fast",Name:"fast",Isolation:core.IsolationContainerTrusted,HostSelector:map[string]string{"lane":"fast"},Resources:core.Resources{CPUMillis:2500,MemoryBytes:4<<30},MaxConcurrency:1}
	slow:=core.RunnerPool{ID:"slow",Name:"slow",Isolation:core.IsolationContainerTrusted,HostSelector:map[string]string{"lane":"slow"},Resources:core.Resources{CPUMillis:2500,MemoryBytes:4<<30},MaxConcurrency:1}
	if err:=st.UpsertPool(ctx,fast);err!=nil{t.Fatal(err)}
	if err:=st.UpsertPool(ctx,slow);err!=nil{t.Fatal(err)}
	for _,h:=range []core.Host{
		{ID:"fast-host",Name:"fast-host",Provider:"docker",State:core.HostOnline,Tags:map[string]string{"lane":"fast","isolation.container-trusted":"true"},Capacity:core.Resources{CPUMillis:6000,MemoryBytes:16<<30}},
		{ID:"slow-host",Name:"slow-host",Provider:"docker",State:core.HostOnline,Tags:map[string]string{"lane":"slow","isolation.container-trusted":"true"},Capacity:core.Resources{CPUMillis:6000,MemoryBytes:16<<30}},
	}{
		if err:=st.UpsertHost(ctx,h);err!=nil{t.Fatal(err)}
	}
	if err:=st.UpsertWorker(ctx,core.Worker{ID:"slow-worker",PoolID:"slow",HostID:"slow-host",ProviderID:"docker",Isolation:core.IsolationContainerTrusted,State:core.WorkerBusy,Resources:slow.Resources,CreatedAt:time.Now().Add(-time.Minute),UpdatedAt:time.Now()});err!=nil{t.Fatal(err)}

	metrics:=&telemetry.Metrics{}
	metrics.Queue("fast",2*time.Second)
	metrics.Queue("slow",90*time.Second)
	svc:=&Service{Store:st,Metrics:metrics}

	read:=func(poolID string)map[string]any{
		req:=httptest.NewRequest(http.MethodGet,"/api/v1/pools/"+poolID+"/recommendation",nil)
		rr:=httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr,req)
		if rr.Code!=http.StatusOK{t.Fatalf("%s status=%d body=%s",poolID,rr.Code,rr.Body.String())}
		var got map[string]any
		if err:=json.NewDecoder(rr.Body).Decode(&got);err!=nil{t.Fatal(err)}
		return got
	}
	fastRec:=read("fast")
	slowRec:=read("slow")
	if int(fastRec["recommended"].(float64))!=1{t.Fatalf("fast recommendation=%v",fastRec)}
	if int(slowRec["recommended"].(float64))!=2{t.Fatalf("slow recommendation=%v",slowRec)}
	if fastRec["averageQueue"]==slowRec["averageQueue"]{t.Fatalf("pool queue averages should differ: fast=%v slow=%v",fastRec,slowRec)}
	slowCapacity:=slowRec["capacity"].(map[string]any)
	if int(slowCapacity["additionalWorkers"].(float64))!=1||int(slowCapacity["capacityLimit"].(float64))!=2{t.Fatalf("slow capacity=%v",slowCapacity)}
}

func TestSummarizePoolLatenciesSeparatesLifecyclePhases(t *testing.T){
	base:=time.Date(2026,9,19,12,0,0,0,time.UTC)
	metrics:=&telemetry.Metrics{}
	metrics.Queue("p",40*time.Second)
	workers:=[]core.Worker{{
		PoolID:"p",State:core.WorkerDeleted,CreatedAt:base,RegistrationStartedAt:base.Add(5*time.Second),RegisteredAt:base.Add(8*time.Second),
		StartedAt:base.Add(50*time.Second),CompletedAt:base.Add(2*time.Minute),UpdatedAt:base.Add(2*time.Minute+7*time.Second),
	}}
	got:=summarizePoolLatencies("p",workers,metrics)
	if got.Queue!="40s"||got.Provision!="5s"||got.Registration!="3s"||got.Execution!="1m10s"||got.Cleanup!="7s"{
		t.Fatalf("latencies=%+v",got)
	}
}
