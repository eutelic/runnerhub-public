package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type reconcileFakeGH struct{
	apiFakeGH
	jobs []githubrunner.WorkflowJob
}
func (b *reconcileFakeGH) ListWorkflowJobs(_ context.Context,_ core.GitHubScope,_ int64)([]githubrunner.WorkflowJob,error){
	return b.jobs,nil
}
func makeReconciliationService(t *testing.T,jobs []githubrunner.WorkflowJob)(*Service,*store.SQLite){
	t.Helper()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
	if err!=nil{t.Fatal(err)}
	svc:=&Service{Store:st,Hub:hub,GitHub:&reconcileFakeGH{jobs:jobs},
		Auth:auth.New(auth.Config{APIKeys:map[string]auth.Principal{
			"admin-key":{Name:"admin",Role:auth.RoleAdmin},
			"viewer-key":{Name:"viewer",Role:auth.RoleViewer},
		}}),
	}
	return svc,st
}
func busyReconcileWorker()core.Worker{
	return core.Worker{
		ID:"worker-old",HostID:"host-1",PoolID:"pool-1",RunnerName:"runnerhub-abc",
		State:core.WorkerBusy,
		Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"eutelic",Repository:"runnerhub"},
		Repository:"eutelic/runnerhub",WorkflowRunID:41211256199,
		JobName:"publish (agent, runnerhub-agent)",
		StartedAt:time.Now().Add(-48*time.Hour),CreatedAt:time.Now().Add(-48*time.Hour),
	}
}
func postReconciliation(svc *Service,key string)*httptest.ResponseRecorder{
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/workers/worker-old/reconcile-github",nil)
	req.Header.Set("Authorization","Bearer "+key)
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	return rr
}

func TestReconcileFinishedGitHubJobReleasesStaleWorkerSlot(t *testing.T){
	job:=githubrunner.WorkflowJob{ID:124571922351,Name:"publish (agent, runnerhub-agent)",
		Status:"completed",Conclusion:"failure",RunnerName:"runnerhub-abc",
		CompletedAt:time.Now().Add(-47*time.Hour)}
	svc,st:=makeReconciliationService(t,[]githubrunner.WorkflowJob{job})
	defer st.Close()
	worker:=busyReconcileWorker()
	if err:=st.UpsertWorker(context.Background(),worker);err!=nil{t.Fatal(err)}
	denied:=postReconciliation(svc,"viewer-key")
	if denied.Code!=http.StatusForbidden{t.Fatalf("viewer status=%d",denied.Code)}
	before,_:=st.GetWorker(context.Background(),worker.ID)
	if before.State!=core.WorkerBusy{t.Fatal("viewer mutated worker")}
	rr:=postReconciliation(svc,"admin-key")
	if rr.Code!=http.StatusOK{t.Fatalf("admin status=%d body=%s",rr.Code,rr.Body.String())}
	got,err:=st.GetWorker(context.Background(),worker.ID)
	if err!=nil{t.Fatal(err)}
	if got.State!=core.WorkerFailed||got.JobResult!="failure"||got.FailedAt.IsZero(){
		t.Fatalf("reconciled worker=%+v",got)
	}
	if got.FailureReason==""||strings.Contains(got.FailureReason,"private key"){
		t.Fatalf("unsafe failure detail=%q",got.FailureReason)
	}
	audit,err:=st.ListAudit(context.Background(),5)
	if err!=nil||len(audit)!=1||audit[0].Action!="reconcile_github_worker"{
		t.Fatalf("audit=%+v err=%v",audit,err)
	}
	again:=postReconciliation(svc,"admin-key")
	if again.Code!=http.StatusConflict{t.Fatalf("second reconciliation=%d",again.Code)}
}

func TestRecoveryOfLegacyBusyWorkerUsingVerifiedJobURL(t *testing.T){
	ctx:=context.Background()
	job:=githubrunner.WorkflowJob{ID:124571922351,Name:"publish (agent, runnerhub-agent)",
		Status:"completed",Conclusion:"failure",RunnerName:"runnerhub-abc",
		CompletedAt:time.Now().Add(-time.Hour)}
	svc,st:=makeReconciliationService(t,[]githubrunner.WorkflowJob{job})
	defer st.Close()
	worker:=busyReconcileWorker()
	worker.WorkflowRunID=0
	if err:=st.UpsertWorker(ctx,worker);err!=nil{t.Fatal(err)}
	url:="https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/124571922351"
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/workers/worker-old/reconcile-github",
		strings.NewReader(`{"githubJobUrl":"`+url+`"}`))
	req.Header.Set("Authorization","Bearer admin-key")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("legacy recovery=%d body=%s",rr.Code,rr.Body.String())}
	got,err:=st.GetWorker(ctx,worker.ID)
	if err!=nil{t.Fatal(err)}
	if got.WorkflowRunID!=41211256199||got.JobID!="124571922351"||
		got.State!=core.WorkerFailed{t.Fatalf("legacy metadata not safely recovered: %+v",got)}
}

func TestRecoveryJobURLRejectsUntrustedHostOrMismatchedEvidence(t *testing.T){
	job:=githubrunner.WorkflowJob{ID:124571922351,Name:"publish (agent, runnerhub-agent)",
		Status:"completed",Conclusion:"failure",RunnerName:"runnerhub-abc",
		CompletedAt:time.Now().Add(-time.Hour)}
	cases:=[]struct{name,url string;want int}{
		{"untrusted hostname","https://github.com.attacker.example/eutelic/runnerhub/actions/runs/41211256199/job/124571922351",http.StatusBadRequest},
		{"untrusted repository","https://github.com/other/runnerhub/actions/runs/41211256199/job/124571922351",http.StatusBadRequest},
		{"query not canonical","https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/124571922351?token=123",http.StatusBadRequest},
		{"mismatched job id","https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/999",http.StatusConflict},
		{"missing job route","https://github.com/eutelic/runnerhub/actions/runs/41211256199",http.StatusBadRequest},
	}
	for _,tc:=range cases{t.Run(tc.name,func(t *testing.T){
		svc,st:=makeReconciliationService(t,[]githubrunner.WorkflowJob{job})
		defer st.Close()
		worker:=busyReconcileWorker()
		worker.WorkflowRunID=0
		if err:=st.UpsertWorker(context.Background(),worker);err!=nil{t.Fatal(err)}
		req:=httptest.NewRequest(http.MethodPost,"/api/v1/workers/worker-old/reconcile-github",
			strings.NewReader(`{"githubJobUrl":"`+tc.url+`"}`))
		req.Header.Set("Authorization","Bearer admin-key")
		rr:=httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr,req)
		if rr.Code!=tc.want{t.Fatalf("status=%d want=%d body=%s",rr.Code,tc.want,rr.Body.String())}
		got,err:=st.GetWorker(context.Background(),worker.ID)
		if err!=nil||got.State!=core.WorkerBusy{t.Fatalf("rejected URL mutated worker: %+v err=%v",got,err)}
	})}
}

func TestReconcileRejectsAmbiguousOrIncompleteGitHubEvidence(t *testing.T){
	cases:=[]struct{name string;jobs []githubrunner.WorkflowJob}{
		{"github still running",[]githubrunner.WorkflowJob{{ID:1,Name:"publish (agent, runnerhub-agent)",RunnerName:"runnerhub-abc",Status:"in_progress"}}},
		{"different runner",[]githubrunner.WorkflowJob{{ID:1,Name:"publish (agent, runnerhub-agent)",RunnerName:"someone-else",Status:"completed",Conclusion:"failure"}}},
		{"duplicate runner match",[]githubrunner.WorkflowJob{{ID:1,Name:"publish (agent, runnerhub-agent)",RunnerName:"runnerhub-abc",Status:"completed",Conclusion:"failure"},{ID:2,Name:"publish (agent, runnerhub-agent)",RunnerName:"runnerhub-abc",Status:"completed",Conclusion:"failure"}}},
		{"missing conclusion",[]githubrunner.WorkflowJob{{ID:1,Name:"publish (agent, runnerhub-agent)",RunnerName:"runnerhub-abc",Status:"completed"}}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			svc,st:=makeReconciliationService(t,tc.jobs)
			defer st.Close()
			worker:=busyReconcileWorker()
			if err:=st.UpsertWorker(context.Background(),worker);err!=nil{t.Fatal(err)}
			rr:=postReconciliation(svc,"admin-key")
			if rr.Code!=http.StatusConflict{t.Fatalf("status=%d body=%s",rr.Code,rr.Body.String())}
			got,err:=st.GetWorker(context.Background(),worker.ID)
			if err!=nil||got.State!=core.WorkerBusy{t.Fatalf("worker mutated=%+v err=%v",got,err)}
		})
	}
}
