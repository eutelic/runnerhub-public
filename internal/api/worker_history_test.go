package api

import (
    "testing"
    "time"
    "github.com/eutelic/runnerhub/internal/core"
)

func TestFilterHistoryPaginationAndSearch(t *testing.T) {
    start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
    workers := []core.Worker{
        {ID:"a",Repository:"eutelic/runnerhub",PoolID:"p1",HostID:"h1",State:core.WorkerFailed,FailureReason:"out of memory",UpdatedAt:start},
        {ID:"b",Repository:"eutelic/runnerhub",PoolID:"p1",HostID:"h2",State:core.WorkerBusy,JobName:"Compile",WorkflowRunID:12345,UpdatedAt:start.Add(2*time.Minute)},
        {ID:"c",Repository:"example-org/mobile-app",PoolID:"p2",State:core.WorkerCompleted,JobResult:"success",UpdatedAt:start.Add(time.Minute)},
    }
    page := filterHistory(workers,historyFilter{Limit:1,Offset:0})
    if page.Total!=3 || !page.HasMore || len(page.Items)!=1 || page.Items[0].ID!="b" {
        t.Fatalf("unexpected first page: %+v",page)
    }
    next := filterHistory(workers,historyFilter{Limit:1,Offset:1})
    if next.Total!=3 || !next.HasMore || next.Items[0].ID!="c" {
        t.Fatalf("unexpected next page: %+v",next)
    }
    failed := filterHistory(workers,historyFilter{Limit:25,State:"failed",Search:"memory"})
    if failed.Total!=1 || failed.Items[0].ID!="a" { t.Fatalf("bad failure search: %+v",failed) }
    byRun := filterHistory(workers,historyFilter{Limit:25,Search:"12345"})
    if byRun.Total!=1 || byRun.Items[0].ID!="b" { t.Fatalf("bad run search: %+v",byRun) }
    byRepo := filterHistory(workers,historyFilter{Limit:25,Repository:"example-org/mobile-app"})
    if byRepo.Total!=1 || byRepo.Items[0].ID!="c" { t.Fatalf("bad repository search: %+v",byRepo) }
    // Ephemeral workers move to deleted after cleanup; retain the job result.
    cleaned := core.Worker{ID:"d",Repository:"eutelic/runnerhub",
        State:core.WorkerDeleted,JobResult:"failure",UpdatedAt:start.Add(3*time.Minute)}
    afterCleanup := filterHistory(append(workers,cleaned),historyFilter{Limit:25,State:"failed"})
    if afterCleanup.Total!=2 || afterCleanup.Items[0].ID!="d" {
        t.Fatalf("cleaned failed job omitted from failure results: %+v",afterCleanup)
    }
    absentRun := filterHistory(workers,historyFilter{Limit:25,Search:"0"})
    if absentRun.Total!=0 {
        t.Fatalf("zero-valued missing run IDs should not match: %+v",absentRun)
    }
    beyond := filterHistory(workers,historyFilter{Limit:25,Offset:99})
    if beyond.Total!=3 || beyond.HasMore || len(beyond.Items)!=0 || beyond.Items==nil { t.Fatalf("bad empty end page: %+v",beyond) }
}
