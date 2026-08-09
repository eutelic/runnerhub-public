package docker

import (
	"context"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"strings"
	"testing"
)
type fakeRunner struct{calls [][]string;out string;err error}
func(f *fakeRunner)Run(_ context.Context,n string,a ...string)(string,error){f.calls=append(f.calls,append([]string{n},a...));return f.out,f.err}
func TestCreateHasNoPortsOrDockerSocketAndHidesJITFromArgs(t *testing.T){f:=&fakeRunner{out:"abc123\n"};p,_:=newWithRunner(Config{StateDir:t.TempDir(),AllowedImages:[]string{"ghcr.io/actions/actions-runner:latest"}},f);s:=provider.WorkerSpec{WorkerID:"w1",PoolID:"p1",RunnerName:"r1",JITConfig:"SECRET-JIT",Image:"ghcr.io/actions/actions-runner:latest",Isolation:core.IsolationContainerTrusted,Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30}};h,err:=p.Create(context.Background(),s);if err!=nil{t.Fatal(err)};if h.ProviderID!="abc123"{t.Fatalf("handle=%+v",h)};joined:=strings.Join(f.calls[0]," ");if strings.Contains(joined,"SECRET-JIT")||strings.Contains(joined,"docker.sock")||strings.Contains(joined," -p "){t.Fatalf("unsafe docker args: %s",joined)};if !strings.Contains(joined,"/run/runnerhub/jitconfig"){t.Fatalf("missing jit secret mount: %s",joined)}}
