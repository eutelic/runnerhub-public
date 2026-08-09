package reconcile

import("context";"testing";"time";"github.com/example-user/runnerhub/internal/core";"github.com/example-user/runnerhub/internal/githubrunner";"github.com/example-user/runnerhub/internal/store")
type missingGH struct{}
func(missingGH)GenerateJITConfig(context.Context,githubrunner.JITConfigRequest)(githubrunner.JITConfig,error){return githubrunner.JITConfig{},nil}
func(missingGH)GetRunner(context.Context,core.GitHubScope,int64)(githubrunner.Runner,error){return githubrunner.Runner{},githubrunner.ErrNotFound}
func(missingGH)DeleteRunner(context.Context,core.GitHubScope,int64)error{return githubrunner.ErrNotFound}
func TestMissingGitHubRunnerFailsDirectWorkerAfterGrace(t *testing.T){ctx:=context.Background();st,_:=store.OpenSQLite(":memory:");defer st.Close();now:=time.Now().UTC();w:=core.Worker{ID:"w",RunnerName:"runner-w",GitHubRunnerID:7,State:core.WorkerReady,UpdatedAt:now.Add(-2*time.Minute)};_ = st.UpsertWorker(ctx,w);r:=&Reconciler{Store:st,GitHub:missingGH{},Now:func()time.Time{return now}};if err:=r.Once(ctx);err!=nil{t.Fatal(err)};got,_:=st.GetWorker(ctx,"w");if got.State!=core.WorkerDeleted{t.Fatalf("missing GitHub registration was not driven through cleanup, state=%s",got.State)}}
func TestRegisteringRunnerGetsShortNotFoundGrace(t *testing.T){ctx:=context.Background();st,_:=store.OpenSQLite(":memory:");defer st.Close();now:=time.Now().UTC();w:=core.Worker{ID:"w",RunnerName:"runner-w",GitHubRunnerID:7,State:core.WorkerRegistering,UpdatedAt:now.Add(-30*time.Second)};_ = st.UpsertWorker(ctx,w);r:=&Reconciler{Store:st,GitHub:missingGH{},Now:func()time.Time{return now}};if err:=r.Once(ctx);err!=nil{t.Fatal(err)};got,_:=st.GetWorker(ctx,"w");if got.State!=core.WorkerRegistering{t.Fatalf("registration grace not honored, state=%s",got.State)}}
