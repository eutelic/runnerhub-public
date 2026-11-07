package fleet

import("sort";"time";"github.com/eutelic/runnerhub/internal/core";"github.com/eutelic/runnerhub/internal/scheduler")
type VersionSummary struct{AgentVersions map[string]int `json:"agentVersions"`;RunnerVersions map[string]int `json:"runnerVersions"`;Images map[string]int `json:"images"`;OfflineHosts int `json:"offlineHosts"`;DrainingHosts int `json:"drainingHosts"`}
func Versions(hosts []core.Host,workers []core.Worker)VersionSummary{s:=VersionSummary{AgentVersions:map[string]int{},RunnerVersions:map[string]int{},Images:map[string]int{}};for _,h:=range hosts{if h.State==core.HostArchived{continue};v:=h.AgentVersion;if v==""{v="unknown"};s.AgentVersions[v]++;if h.State==core.HostOffline{s.OfflineHosts++};if h.State==core.HostDraining{s.DrainingHosts++}};for _,w:=range workers{if w.State==core.WorkerDeleted{continue};s.Images[w.Image]++;v:=w.GitHubRunnerVersion;if v==""{v="unknown"};s.RunnerVersions[v]++};return s}
type Health struct{PoolID string `json:"poolId"`;OnlineHosts int `json:"onlineHosts"`;ActiveWorkers int `json:"activeWorkers"`;BusyWorkers int `json:"busyWorkers"`;CapacityLimit int `json:"capacityLimit"`;Healthy bool `json:"healthy"`;Reasons []string `json:"reasons,omitempty"`}
func PoolHealth(pool core.RunnerPool,hosts []core.Host,workers []core.Worker,now time.Time)Health{h:=Health{PoolID:pool.ID};used:=map[string]core.Resources{};capacities:=map[string]core.Resources{};hostByID:=map[string]core.Host{};for _,host:=range hosts{hostByID[host.ID]=host;if host.State!=core.HostOnline{continue};key:=scheduler.PlacementDomainKey(host);if cap,ok:=capacities[key];ok{capacities[key]=minResources(cap,host.Capacity)}else{capacities[key]=host.Capacity}};for _,w:=range workers{if scheduler.OccupiesHostResources(w){key:=scheduler.PlacementDomainForWorker(hostByID,w.HostID);used[key]=core.Add(used[key],scheduler.EffectiveWorkerResources(w))};if w.PoolID!=pool.ID{continue};if scheduler.CountsTowardPoolConcurrency(w.State){h.ActiveWorkers++};if w.State==core.WorkerBusy{h.BusyWorkers++}};var rejects []string;for _,host:=range hosts{if now.Sub(host.LastSeenAt)>=time.Minute{continue};key:=scheduler.PlacementDomainKey(host);if reason:=scheduler.HostRejectionReasonWithCapacity(host,pool,used[key],capacities[key]);reason==""{h.OnlineHosts++}else{rejects=append(rejects,host.Name+": "+reason)}};if h.OnlineHosts==0{if len(rejects)>0{h.Reasons=append(h.Reasons,rejects...)}else{h.Reasons=append(h.Reasons,"no recently online hosts")}};if h.ActiveWorkers>pool.MaxConcurrency{h.Reasons=append(h.Reasons,"active workers exceed pool concurrency")};h.Healthy=len(h.Reasons)==0;h.CapacityLimit=EligibleCapacityCeiling(pool,hosts,workers,now);sort.Strings(h.Reasons);return h}
func minResources(a,b core.Resources)core.Resources{min:=func(x,y int64)int64{if x<y{return x};return y};gpus:=a.GPUs;if b.GPUs<gpus{gpus=b.GPUs};return core.Resources{CPUMillis:min(a.CPUMillis,b.CPUMillis),MemoryBytes:min(a.MemoryBytes,b.MemoryBytes),DiskBytes:min(a.DiskBytes,b.DiskBytes),GPUs:gpus}}

// EligibleCapacityCeiling is a conservative per-pool upper bound that includes
// live reservations plus additional physical slots on recently seen hosts.
// It is NOT a global claim for concurrent pools that compete for the same
// unreserved physical slot; the server-side admission lock remains mandatory.
func EligibleCapacityCeiling(pool core.RunnerPool, hosts []core.Host, workers []core.Worker, now time.Time) int {
	// Keep the full host identity ledger for reservation accounting.
	// Filtering stale/archived aliases out before SummarizeCapacity loses
	// placement-domain and merge mappings for workers still assigned there,
	// potentially advertising the same physical capacity twice.
	eligible := append([]core.Host(nil), hosts...)
	for i := range eligible {
		host := &eligible[i]
		if host.State == core.HostOnline &&
			(host.LastSeenAt.IsZero() || now.Sub(host.LastSeenAt) >= time.Minute) {
			host.State = core.HostOffline
		}
	}
	limit := scheduler.SummarizeCapacity(pool, eligible, workers).CapacityLimit
	if limit < 0 { limit = 0 }
	if pool.MaxConcurrency < 0 { return 0 }
	if limit > pool.MaxConcurrency { return pool.MaxConcurrency }
	return limit
}
