package agenthub

import("context";"testing";"github.com/eutelic/runnerhub/internal/agent";"github.com/eutelic/runnerhub/internal/provider";"github.com/eutelic/runnerhub/internal/store")
func TestEnrollAndHeartbeat(t *testing.T){st,_:=store.OpenSQLite(":memory:");defer st.Close();h,err:=New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef");if err!=nil{t.Fatal(err)};req:=agent.EnrollRequest{Name:"lab",Provider:"docker",Capabilities:provider.Capabilities{Provider:"docker",OS:"linux",Architecture:"amd64"}};res,err:=h.Enroll(context.Background(),"0123456789abcdef",req);if err!=nil{t.Fatal(err)};if res.HostID==""||!h.Authenticate(res.HostID,res.AgentToken){t.Fatal("enrolled token did not authenticate")};if err:=h.Heartbeat(context.Background(),agent.Heartbeat{HostID:res.HostID,Capabilities:req.Capabilities});err!=nil{t.Fatal(err)}}
func TestSameHostnameEnrollmentsDoNotCollide(t *testing.T){st,_:=store.OpenSQLite(":memory:");defer st.Close();h,_:=New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef");req:=agent.EnrollRequest{Name:"duplicated-hostname",Provider:"docker",Capabilities:provider.Capabilities{Provider:"docker"}};a,err:=h.Enroll(context.Background(),"0123456789abcdef",req);if err!=nil{t.Fatal(err)};b,err:=h.Enroll(context.Background(),"0123456789abcdef",req);if err!=nil{t.Fatal(err)};if a.HostID==b.HostID{t.Fatalf("duplicate hostname reused host id %q",a.HostID)};hosts,err:=st.ListHosts(context.Background());if err!=nil{t.Fatal(err)};if len(hosts)!=2{t.Fatalf("expected two distinct hosts, got %d",len(hosts))}}

func TestHeartbeatRefreshesAgentVersion(t *testing.T){st,_:=store.OpenSQLite(":memory:");defer st.Close();h,err:=New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef");if err!=nil{t.Fatal(err)};req:=agent.EnrollRequest{Name:"lab",Provider:"docker",AgentVersion:"old",Capabilities:provider.Capabilities{Provider:"docker",OS:"linux",Architecture:"amd64"}};res,err:=h.Enroll(context.Background(),"0123456789abcdef",req);if err!=nil{t.Fatal(err)};if err:=h.Heartbeat(context.Background(),agent.Heartbeat{HostID:res.HostID,AgentVersion:"new",Capabilities:req.Capabilities});err!=nil{t.Fatal(err)};host,err:=st.GetHost(context.Background(),res.HostID);if err!=nil{t.Fatal(err)};if host.AgentVersion!="new"{t.Fatalf("agent version=%q, want new",host.AgentVersion)}}


func TestHeartbeatPreservesHostPriority(t *testing.T){
	st,_:=store.OpenSQLite(":memory:")
	defer st.Close()
	h,err:=New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
	if err!=nil{t.Fatal(err)}
	req:=agent.EnrollRequest{Name:"priority-host",Provider:"docker",Capabilities:provider.Capabilities{Provider:"docker",OS:"linux",Architecture:"amd64"}}
	res,err:=h.Enroll(context.Background(),"0123456789abcdef",req)
	if err!=nil{t.Fatal(err)}
	host,err:=st.GetHost(context.Background(),res.HostID)
	if err!=nil{t.Fatal(err)}
	host.Priority=100
	if err:=st.UpsertHost(context.Background(),host);err!=nil{t.Fatal(err)}
	if err:=h.Heartbeat(context.Background(),agent.Heartbeat{HostID:res.HostID,Capabilities:req.Capabilities});err!=nil{t.Fatal(err)}
	got,err:=st.GetHost(context.Background(),res.HostID)
	if err!=nil{t.Fatal(err)}
	if got.Priority!=100{t.Fatalf("priority=%d, want 100 after heartbeat",got.Priority)}
}
