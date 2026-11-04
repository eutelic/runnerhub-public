//go:build !localtest

package store

import (
    "context"
    "fmt"
    "strings"
    "testing"

    "github.com/eutelic/runnerhub/internal/core"
)

func TestNonDeletedWorkersUsePartialIndexAndPreserveHistory(t *testing.T) {
    ctx := context.Background()
    db, err := OpenSQLite(":memory:")
    if err != nil { t.Fatal(err) }
    defer db.Close()

    // A historical ledger should not be decoded by live control paths.
    for i := 0; i < 512; i++ {
        w := core.Worker{ID:fmt.Sprintf("historical-%04d",i),State:core.WorkerDeleted}
        if err := db.UpsertWorker(ctx,w); err != nil { t.Fatal(err) }
    }
    cases := []core.Worker{
        {ID:"busy",State:core.WorkerBusy},
        {ID:"cleanup",State:core.WorkerCleanupPending},
        {ID:"failed",State:core.WorkerFailed},
        {ID:"completed",State:core.WorkerCompleted},
    }
    for _,w := range cases {
        if err := db.UpsertWorker(ctx,w); err != nil { t.Fatal(err) }
    }

    live, err := db.ListNonDeletedWorkers(ctx)
    if err != nil { t.Fatal(err) }
    if len(live) != len(cases) { t.Fatalf("non-deleted workers=%d want %d",len(live),len(cases)) }
    got := map[string]core.WorkerState{}
    for _,w := range live { got[w.ID]=w.State }
    for _,w := range cases {
        if got[w.ID] != w.State { t.Fatalf("worker %s missing or wrong state: %v",w.ID,got[w.ID]) }
    }

    // Neither the ordinary full-ledger API nor paginated history is truncated.
    all, err := db.ListWorkers(ctx)
    if err != nil { t.Fatal(err) }
    if len(all) != 516 { t.Fatalf("historical ledger lost records: %d",len(all)) }
    page,err := db.QueryWorkerHistory(ctx, WorkerHistoryQuery{Limit:25})
    if err != nil { t.Fatal(err) }
    if page.Total != 516 { t.Fatalf("history total=%d want 516",page.Total) }

    rows, err := db.db.QueryContext(ctx,"EXPLAIN QUERY PLAN "+nonDeletedWorkersQuery)
    if err != nil { t.Fatal(err) }
    indexUsed := false
    for rows.Next() {
        var id,parent,unused int
        var detail string
        if err := rows.Scan(&id,&parent,&unused,&detail); err != nil { t.Fatal(err) }
        indexUsed = indexUsed || strings.Contains(detail,"idx_workers_non_deleted")
    }
    if err := rows.Err(); err != nil { t.Fatal(err) }
    if err := rows.Close(); err != nil { t.Fatal(err) }
    if !indexUsed { t.Fatal("non-deleted snapshot did not use partial index") }

    // A transitioned worker leaves the live snapshot but remains in history.
    cases[0].State=core.WorkerDeleted
    if err := db.UpsertWorker(ctx,cases[0]); err != nil { t.Fatal(err) }
    live,err=db.ListNonDeletedWorkers(ctx)
    if err!=nil {t.Fatal(err)}
    if len(live)!=3 {t.Fatalf("non-deleted after transition=%d want 3",len(live))}
    all,err=db.ListWorkers(ctx)
    if err!=nil {t.Fatal(err)}
    if len(all)!=516 {t.Fatalf("transition removed a historical record: %d",len(all))}
}
