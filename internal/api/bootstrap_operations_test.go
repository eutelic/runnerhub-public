package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/machinebootstrap"
	"github.com/eutelic/runnerhub/internal/store"
)

type blockedOperationBootstrap struct{
	started chan struct{}
	release chan struct{}
	calls atomic.Int32
}

func (b *blockedOperationBootstrap) Bootstrap(ctx context.Context,req machinebootstrap.Request,_ string,_ machinebootstrap.WaitForHost)(core.Host,error){
	b.calls.Add(1)
	select{case b.started<-struct{}{}:default:}
	select{
	case <-b.release:
		return core.Host{ID:"host-recovered",Name:req.HostName,Provider:"docker",State:core.HostOnline},nil
	case <-ctx.Done():
		return core.Host{},ctx.Err()
	}
}

func operationService(t *testing.T)(*Service,*store.SQLite,*blockedOperationBootstrap){
	t.Helper()
	st,err:=store.OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
	if err!=nil{t.Fatal(err)}
	backend:=&blockedOperationBootstrap{started:make(chan struct{},2),release:make(chan struct{})}
	svc:=&Service{
		Store:st,Hub:hub,MachineBootstrap:backend,
		MachineBootstrapURL:"https://runnerhub.example",
		Auth:auth.New(auth.Config{APIKeys:map[string]auth.Principal{
			"admin-secret":{Name:"test-admin",Role:auth.RoleAdmin},
			"viewer-secret":{Name:"reader",Role:auth.RoleViewer},
		}}),
	}
	return svc,st,backend
}

func postOperation(t *testing.T,svc *Service,secret string)*httptest.ResponseRecorder{
	t.Helper()
	body:=[]byte(`{"address":"compute.example","user":"compute","name":"compute",
	"hostKeyFingerprint":"SHA256:verified","password":"password-shh",
	"registryPassword":"registry-secret","privateKey":"key-secret","priority":3}`)
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/hosts/bootstrap-operations",bytes.NewReader(body))
	req.Header.Set("Authorization","Bearer "+secret)
	req.Header.Set("Content-Type","application/json")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	return rr
}

func TestTrackedEnrollmentIsDeduplicatedAndContainsNoCredentials(t *testing.T){
	svc,st,backend:=operationService(t)
	defer st.Close()
	first:=postOperation(t,svc,"admin-secret")
	if first.Code!=http.StatusAccepted{t.Fatalf("first=%d: %s",first.Code,first.Body.String())}
	var started struct{store.Operation;Reused bool `json:"reused"`}
	if err:=json.Unmarshal(first.Body.Bytes(),&started);err!=nil{t.Fatal(err)}
	if started.ID==""||started.Reused{t.Fatalf("unexpected first op=%+v",started)}
	select{
	case <-backend.started:
	case <-time.After(2*time.Second):t.Fatal("bootstrap never started")
	}
	second:=postOperation(t,svc,"admin-secret")
	if second.Code!=http.StatusAccepted{t.Fatalf("second=%d: %s",second.Code,second.Body.String())}
	var reused struct{store.Operation;Reused bool `json:"reused"`}
	if err:=json.Unmarshal(second.Body.Bytes(),&reused);err!=nil{t.Fatal(err)}
	if !reused.Reused||reused.ID!=started.ID{t.Fatalf("not deduplicated: first=%+v second=%+v",started,reused)}
	if backend.calls.Load()!=1{t.Fatalf("duplicate bootstrap started, calls=%d",backend.calls.Load())}
	for _,leak:=range []string{"password-shh","registry-secret","key-secret"}{
		if strings.Contains(first.Body.String(),leak)||strings.Contains(second.Body.String(),leak){
			t.Fatalf("secret %q leaked through status response",leak)
		}
	}
	denied:=postOperation(t,svc,"viewer-secret")
	if denied.Code!=http.StatusForbidden{t.Fatalf("viewer bootstrap=%d",denied.Code)}
	close(backend.release)
	deadline:=time.Now().Add(3*time.Second)
	for time.Now().Before(deadline){
		op,err:=st.GetOperation(context.Background(),started.ID)
		if err!=nil{t.Fatal(err)}
		if op.State=="succeeded"{
			if op.HostID!="host-recovered"||op.Step!="agent-ready"{t.Fatalf("result=%+v",op)}
			data,_:=json.Marshal(op)
			for _,leak:=range []string{"password-shh","registry-secret","key-secret"}{
				if strings.Contains(string(data),leak){t.Fatalf("secret leaked to persistence: %q",leak)}
			}
			return
		}
		time.Sleep(10*time.Millisecond)
	}
	t.Fatal("tracked enrollment did not finish")
}

func TestOperationReadRequiresAdminAndReturnsHistory(t *testing.T){
	svc,st,backend:=operationService(t)
	defer st.Close()
	first:=postOperation(t,svc,"admin-secret")
	var op store.Operation
	if err:=json.Unmarshal(first.Body.Bytes(),&op);err!=nil{t.Fatal(err)}
	for _,tt:=range []struct{secret string;want int}{
		{"viewer-secret",http.StatusForbidden},{"admin-secret",http.StatusOK},
	}{
		req:=httptest.NewRequest(http.MethodGet,"/api/v1/operations/"+op.ID,nil)
		req.Header.Set("Authorization","Bearer "+tt.secret)
		rr:=httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr,req)
		if rr.Code!=tt.want{t.Fatalf("read role=%s status=%d body=%s",tt.secret,rr.Code,rr.Body.String())}
	}
	req:=httptest.NewRequest(http.MethodGet,"/api/v1/operations?limit=10",nil)
	req.Header.Set("Authorization","Bearer admin-secret")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK||!strings.Contains(rr.Body.String(),op.ID){
		t.Fatalf("list=%d: %s",rr.Code,rr.Body.String())
	}
	close(backend.release)
}

func TestLegacyBootstrapCannotRaceTrackedEnrollment(t *testing.T){
	svc,st,backend:=operationService(t)
	defer st.Close()
	first:=postOperation(t,svc,"admin-secret")
	if first.Code!=http.StatusAccepted{t.Fatalf("tracked=%d",first.Code)}
	var op store.Operation
	if err:=json.Unmarshal(first.Body.Bytes(),&op);err!=nil{t.Fatal(err)}
	select{case <-backend.started:case <-time.After(2*time.Second):t.Fatal("bootstrap never started")}
	body:=[]byte(`{"address":"compute.example","user":"compute","name":"compute","hostKeyFingerprint":"SHA256:verified"}`)
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/hosts/bootstrap",bytes.NewReader(body))
	req.Header.Set("Authorization","Bearer admin-secret")
	rr:=httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusConflict||!strings.Contains(rr.Body.String(),op.ID){
		t.Fatalf("legacy race status=%d body=%s",rr.Code,rr.Body.String())
	}
	if backend.calls.Load()!=1{t.Fatalf("extra SSH invocation: %d",backend.calls.Load())}
	close(backend.release)
}

type partialCleanupBootstrap struct{}
func (partialCleanupBootstrap) Bootstrap(_ context.Context,req machinebootstrap.Request,_ string,_ machinebootstrap.WaitForHost)(core.Host,error){
	return core.Host{ID:"host-partial",Name:req.HostName,Provider:"docker",State:core.HostOnline},
		errors.New("SENSITIVE_REMOTE_OUTPUT_DO_NOT_PERSIST")
}

func TestPartialEnrollmentIsBlockedAndDoesNotLeakRemoteOutput(t *testing.T){
	svc,st,_:=operationService(t)
	defer st.Close()
	svc.MachineBootstrap=partialCleanupBootstrap{}
	rr:=postOperation(t,svc,"admin-secret")
	if rr.Code!=http.StatusAccepted{t.Fatalf("request status=%d body=%s",rr.Code,rr.Body.String())}
	var requested store.Operation
	if err:=json.Unmarshal(rr.Body.Bytes(),&requested);err!=nil{t.Fatal(err)}
	deadline:=time.Now().Add(3*time.Second)
	for time.Now().Before(deadline){
		op,err:=st.GetOperation(context.Background(),requested.ID)
		if err!=nil{t.Fatal(err)}
		if op.State=="blocked"{
			if op.HostID!="host-partial"||op.Step!="enrollment-cleanup-failed"{
				t.Fatalf("partial host lost: %+v",op)
			}
			if strings.Contains(op.Detail,"SENSITIVE_REMOTE_OUTPUT"){
				t.Fatal("remote output leaked through operation details")
			}
			return
		}
		time.Sleep(10*time.Millisecond)
	}
	t.Fatal("partial enrollment was not marked blocked")
}
