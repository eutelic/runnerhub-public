package store

import (
    "context"
    "github.com/eutelic/runnerhub/internal/core"
)

// WorkerHistoryQuery is an optional read-only store capability. Keep it
// separate from Store so existing test and alternative stores keep working.
type WorkerHistoryQuery struct {
    Repository string
    Pool string
    Host string
    State string
    Search string
    Limit int
    Offset int
}

type WorkerHistoryPage struct {
    Items []core.Worker
    Total int
    Limit int
    Offset int
    HasMore bool
}

type WorkerHistoryQuerier interface {
    QueryWorkerHistory(context.Context, WorkerHistoryQuery) (WorkerHistoryPage, error)
}
