package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/actions/scaleset"
)

func TestDesiredRunnerCountRejectsInactiveOrMismatchedCreateResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		state string
		githubRunnerID int64
		poolID string
	}{
		{name: "deleted", state: "deleted", githubRunnerID: 777, poolID: "pool-docker"},
		{name: "failed", state: "failed", githubRunnerID: 777, poolID: "pool-docker"},
		{name: "completed", state: "completed", githubRunnerID: 777, poolID: "pool-docker"},
		{name: "wrong GitHub runner", state: "provisioning", githubRunnerID: 999, poolID: "pool-docker"},
		{name: "wrong pool", state: "provisioning", githubRunnerID: 777, poolID: "pool-other"},
	} {
		t.Run(tc.name,func(t *testing.T){
			srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				switch r.URL.Path {
				case "/api/v1/workers":
					_ = json.NewEncoder(w).Encode([]workerResponse{})
				case "/api/v1/pools/pool-docker/health":
					_ = json.NewEncoder(w).Encode(poolHealthResponse{Healthy: true})
				case "/api/v1/pools/pool-docker/scale-workers":
					var request struct{RunnerName string `json:"runnerName"`}
					if err:=json.NewDecoder(r.Body).Decode(&request);err!=nil{
						http.Error(w, "bad request", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(workerResponse{
						ID:"old-worker",PoolID:tc.poolID,RunnerName:request.RunnerName,
						State:tc.state,GitHubRunnerID:tc.githubRunnerID,
					})
				default:
					http.NotFound(w,r)
				}
			}))
			defer srv.Close()
			gh:=&fakeScaleSetRunnerClient{jit:&scaleset.RunnerScaleSetJitRunnerConfig{
				Runner:&scaleset.RunnerReference{ID:777,Name:"old-runner"},
				EncodedJITConfig:"encoded",
			}}
			s:=&scaler{
				names:map[string]string{},min:0,max:1,scaleSetID:7,github:gh,
				hub:runnerHubClient{base:srv.URL,poolID:"pool-docker",http:srv.Client()},
				logger:slog.Default(),
			}
			count,err:=s.HandleDesiredRunnerCount(context.Background(),1)
			if err==nil||!strings.Contains(err.Error(),"inactive or mismatched"){
				t.Fatalf("unsafe create response was treated as accepted: count=%d err=%v",count,err)
			}
			if count!=0||len(s.names)!=0 {
				t.Fatalf("phantom active runner after rejected response: count=%d names=%+v",count,s.names)
			}
			if len(gh.removeCalls)!=1||gh.removeCalls[0]!=777 {
				t.Fatalf("unused GitHub JIT registration not cleaned: %+v",gh.removeCalls)
			}
		})
	}
}
