package main

import(
 "context"
 "github.com/eutelic/runnerhub/internal/provider"
)

// Only hosts that have verified a detached, pinned updater image at startup
// advertise this capability to the control plane. A newer revision alone is
// not sufficient for a safe one-click host-agent upgrade.
type updateCapableProvider struct{ provider.Provider }
func(p updateCapableProvider)Capabilities(ctx context.Context)(provider.Capabilities,error){
 cap,err:=p.Provider.Capabilities(ctx)
 if err!=nil{return cap,err}
 if cap.Features==nil{cap.Features=map[string]bool{}}
 cap.Features["agentSelfUpdate"]=true
 return cap,nil
}
