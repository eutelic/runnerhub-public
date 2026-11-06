package agentupdate

import(
 "context"
 "testing"
 "time"
)

type blockedHeartbeat struct{*fakeAdapter}
func(b blockedHeartbeat)WaitForHeartbeat(ctx context.Context,_ string,_ string,_ time.Time)error{
  <-ctx.Done()
  return ctx.Err()
}

func TestUnresponsiveNewAgentIsRolledBackAfterBoundedHeartbeatWait(t *testing.T){
  adapter:=blockedHeartbeat{&fakeAdapter{}}
  start:=time.Now()
  out:=Run(context.Background(),Request{
    HostID:"host-one",Target:validTarget(),
    HeartbeatTimeout:10*time.Millisecond,
    RecoveryTimeout:time.Second,
  },adapter,&fakeGate{},nil)
  if out.State!="failed"||out.Step!="rolled-back"{t.Fatalf("missing safe rollback: %+v",out)}
  if elapsed:=time.Since(start);elapsed>time.Second{t.Fatalf("heartbeat recovery was not bounded: %v",elapsed)}
  if adapter.calls[len(adapter.calls)-1]!="restore"{t.Fatalf("old agent not restored: %v",adapter.calls)}
}
