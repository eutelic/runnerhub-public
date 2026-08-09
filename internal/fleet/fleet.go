package fleet

import (
	"github.com/example-user/runnerhub/internal/core"
	"sort"
	"time"
)

type VersionSummary struct {
	AgentVersions map[string]int `json:"agentVersions"`
	Images        map[string]int `json:"images"`
	OfflineHosts  int            `json:"offlineHosts"`
	DrainingHosts int            `json:"drainingHosts"`
}

func Versions(hosts []core.Host, workers []core.Worker) VersionSummary {
	s := VersionSummary{AgentVersions: map[string]int{}, Images: map[string]int{}}
	for _, h := range hosts {
		s.AgentVersions[h.AgentVersion]++
		if h.State == core.HostOffline { s.OfflineHosts++ }
		if h.State == core.HostDraining { s.DrainingHosts++ }
	}
	for _, w := range workers { if w.State != core.WorkerDeleted { s.Images[w.Image]++ } }
	return s
}

type Health struct { PoolID string `json:"poolId"`; OnlineHosts int `json:"onlineHosts"`; ActiveWorkers int `json:"activeWorkers"`; BusyWorkers int `json:"busyWorkers"`; Healthy bool `json:"healthy"`; Reasons []string `json:"reasons,omitempty"` }

func PoolHealth(pool core.RunnerPool, hosts []core.Host, workers []core.Worker, now time.Time) Health {
	h := Health{PoolID: pool.ID}
	for _, host := range hosts { if host.State == core.HostOnline && match(pool.HostSelector, host.Tags) && now.Sub(host.LastSeenAt) < time.Minute { h.OnlineHosts++ } }
	for _, w := range workers { if w.PoolID != pool.ID || w.State == core.WorkerDeleted { continue }; h.ActiveWorkers++; if w.State == core.WorkerBusy { h.BusyWorkers++ } }
	if h.OnlineHosts == 0 { h.Reasons = append(h.Reasons, "no online compatible hosts") }
	if h.ActiveWorkers > pool.MaxConcurrency { h.Reasons = append(h.Reasons, "active workers exceed pool concurrency") }
	h.Healthy = len(h.Reasons) == 0; sort.Strings(h.Reasons); return h
}
func match(sel, tags map[string]string) bool { for k,v := range sel { if tags[k] != v { return false } }; return true }
