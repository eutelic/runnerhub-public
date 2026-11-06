package api

import (
    "errors"
    "net/http"
    "regexp"
    "strings"

    "github.com/eutelic/runnerhub/internal/auth"
    "github.com/eutelic/runnerhub/internal/agentupdate"
    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/scheduler"
    "github.com/eutelic/runnerhub/internal/store"
)

// AgentUpdatePreflight is an observation, NOT authorization to replace a
// container. An updater must revalidate all constraints while holding an
// exclusive, durable host-operation lease before it can act.
type AgentUpdatePreflight struct {
    HostID string `json:"hostId"`
    HostName string `json:"hostName"`
    Provider string `json:"provider"`
    HostState core.HostState `json:"hostState"`
    InstalledRevision string `json:"installedRevision"`
    ConfiguredTargetImage string `json:"configuredTargetImage"`
    ConfiguredTargetRevision string `json:"configuredTargetRevision,omitempty"`
    TargetMetadataValid bool `json:"targetMetadataValid"`
    ImmutableTarget bool `json:"immutableTarget"`
    ActiveWorkers int `json:"activeWorkers"`
    CleanupReservations int `json:"cleanupReservations"`
    UpdaterAdvertised bool `json:"updaterAdvertised"`
    CanUpdate bool `json:"canUpdate"`
    Blockers []string `json:"blockers"`
}

// Pinning protects against mutable tags but is not evidence that an image
// passed the release pipeline. Do not accept an arbitrary registry target.
var runnerhubAgentDigest = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent@sha256:[a-f0-9]{64}$`)

func makeAgentUpdatePreflight(host core.Host, workers []core.Worker, target string, revision ...string) AgentUpdatePreflight {
    p := AgentUpdatePreflight{
        HostID:host.ID, HostName:host.Name, Provider:host.Provider,
        HostState:host.State, InstalledRevision:host.AgentVersion,
        ConfiguredTargetImage:strings.TrimSpace(target),
        Blockers:[]string{},
    }
    if len(revision)>0{p.ConfiguredTargetRevision=strings.TrimSpace(revision[0])}
    p.ImmutableTarget = runnerhubAgentDigest.MatchString(p.ConfiguredTargetImage)
    p.TargetMetadataValid=(agentupdate.Target{Image:p.ConfiguredTargetImage,Revision:p.ConfiguredTargetRevision}).Validate()==nil
    p.UpdaterAdvertised = host.SystemTags["feature.agentSelfUpdate"] == "true"
    if host.Provider != "docker" {
        p.Blockers = append(p.Blockers,"non-docker-agent")
    }
    if host.State != core.HostOnline {
        p.Blockers = append(p.Blockers,"host-not-online")
    }
    for _, worker := range workers {
        if worker.HostID != host.ID || worker.State == core.WorkerDeleted {
            continue
        }
        if scheduler.OccupiesHostResources(worker) {
            p.ActiveWorkers++
            switch worker.State {
            case core.WorkerFailed, core.WorkerCompleted, core.WorkerCleanupPending, core.WorkerDeleting:
                p.CleanupReservations++
            }
        }
    }
    if p.ActiveWorkers > 0 {
        p.Blockers = append(p.Blockers,"active-workers-or-cleanup")
    }
    if !p.ImmutableTarget {
        p.Blockers = append(p.Blockers,"immutable-verified-target-required")
    }
    if !p.TargetMetadataValid {p.Blockers=append(p.Blockers,"verified-release-metadata-required")}
    if !p.UpdaterAdvertised {
        p.Blockers = append(p.Blockers,"agent-updater-not-advertised")
    }
    // This first increment exposes trustworthy operator evidence. Do not
    // claim the update is available before a detached updater, durable
    // operation record, rollback, and post-restart heartbeat exist.
    p.Blockers = append(p.Blockers,"update-executor-not-implemented")
    p.CanUpdate = false
    return p
}

func (s *Service) agentUpdatePreflight(w http.ResponseWriter, r *http.Request) {
    if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
        problem(w,http.StatusForbidden,errors.New("agent update inspection requires admin access"))
        return
    }
    h,err:=s.Store.GetHost(r.Context(),r.PathValue("id"))
    if err!=nil {
        if errors.Is(err,store.ErrNotFound) {problem(w,http.StatusNotFound,err)} else {problem(w,http.StatusInternalServerError,err)}
        return
    }
    workers,err:=s.Store.ListWorkers(r.Context())
    if err!=nil {problem(w,http.StatusInternalServerError,err);return}
    p:=makeAgentUpdatePreflight(h,workers,s.AgentUpdateTargetImage,s.AgentUpdateTargetRevision)
    if !s.AgentUpdateEnabled {
        p.Blockers=append(p.Blockers,"agent-update-feature-disabled")
    }else if !officialUpdaterDigest.MatchString(s.AgentUpdateHelperImage){
        p.Blockers=append(p.Blockers,"pinned-helper-image-unavailable")
    }else{
        hardBlocked:=false
        filtered:=make([]string,0,len(p.Blockers))
        for _,blocker:=range p.Blockers{
            if blocker=="update-executor-not-implemented"{continue}
            filtered=append(filtered,blocker)
            // Existing jobs can drain naturally. They are a delay, not
            // a reason to reject an otherwise safe one-click request.
            if blocker!="active-workers-or-cleanup"{hardBlocked=true}
        }
        p.Blockers=filtered
        if h.AgentVersion==s.AgentUpdateTargetRevision{
            p.Blockers=append(p.Blockers,"agent-already-current")
            hardBlocked=true
        }
        p.CanUpdate=!hardBlocked
    }
    writeJSON(w,http.StatusOK,p)
}

