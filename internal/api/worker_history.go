package api

import (
    "net/http"
    "sort"
    "strconv"
    "strings"

    "github.com/eutelic/runnerhub/internal/auth"
    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/store"
)

// workerHistoryPage is deliberately separate from the live worker snapshot API.
// SQLite filters and pages within the database. Other stores fall back to an
// in-memory pass; either way, only the requested window reaches the browser.
type workerHistoryPage struct {
    Items []core.Worker `json:"items"`
    Total int `json:"total"`
    Limit int `json:"limit"`
    Offset int `json:"offset"`
    HasMore bool `json:"hasMore"`
}

type historyFilter struct {
    Repository string
    Pool string
    Host string
    State string
    Search string
    Limit int
    Offset int
}

func (s *Service) workerHistory(w http.ResponseWriter, r *http.Request) {
    q := r.URL.Query()
    limit := 25
    if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
        value, err := strconv.Atoi(raw)
        if err != nil || value < 1 || value > 100 {
            problem(w, 400, errInvalidHistoryParameter("limit must be between 1 and 100"))
            return
        }
        limit = value
    }
    offset := 0
    if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
        value, err := strconv.Atoi(raw)
        if err != nil || value < 0 || value > 100000 {
            problem(w, 400, errInvalidHistoryParameter("offset must be between 0 and 100000"))
            return
        }
        offset = value
    }
    query := store.WorkerHistoryQuery{
        Repository:q.Get("repository"), Pool:q.Get("pool"),
        Host:q.Get("host"), State:q.Get("state"),
        Search:q.Get("search"), Limit:limit, Offset:offset,
    }
    var page workerHistoryPage
    if indexed, ok := s.Store.(store.WorkerHistoryQuerier); ok {
        result, err := indexed.QueryWorkerHistory(r.Context(),query)
        if err != nil { problem(w,500,err);return }
        page = workerHistoryPage{Items:result.Items,Total:result.Total,
            Limit:result.Limit,Offset:result.Offset,HasMore:result.HasMore}
    } else {
        workers, err := s.Store.ListWorkers(r.Context())
        if err != nil { problem(w,500,err);return }
        page = filterHistory(workers,historyFilter{
            Repository:query.Repository,Pool:query.Pool,Host:query.Host,
            State:query.State,Search:query.Search,Limit:query.Limit,Offset:query.Offset,
        })
    }
    if auth.PrincipalFrom(r.Context()).Role == auth.RoleScaler {
        for i := range page.Items { page.Items[i].ProviderDiagnostics="" }
    }
    writeJSON(w, 200, page)
}

type historyParameterError string
func (e historyParameterError) Error() string { return string(e) }
func errInvalidHistoryParameter(text string) error { return historyParameterError(text) }

func filterHistory(workers []core.Worker, filter historyFilter) workerHistoryPage {
    selected := make([]core.Worker, 0)
    contains := func(s, v string) bool {
        return strings.Contains(strings.ToLower(s), strings.ToLower(strings.TrimSpace(v)))
    }
    for _, wk := range workers {
        repository := wk.Repository
        if repository == "" && wk.Scope.Owner != "" && wk.Scope.Repository != "" {
            repository = wk.Scope.Owner + "/" + wk.Scope.Repository
        }
        if filter.Repository != "" && !strings.EqualFold(repository, strings.TrimSpace(filter.Repository)) { continue }
        if filter.Pool != "" && wk.PoolID != strings.TrimSpace(filter.Pool) { continue }
        if filter.Host != "" && wk.HostID != strings.TrimSpace(filter.Host) { continue }
        if filter.State != "" && !workerMatchesHistoryState(wk, filter.State) { continue }
        if filter.Search != "" && !(contains(repository, filter.Search) ||
            contains(wk.JobName, filter.Search) || contains(wk.FailureReason, filter.Search) ||
            contains(wk.ID, filter.Search) || contains(wk.RunnerName, filter.Search) ||
            (wk.WorkflowRunID>0 && contains(strconv.FormatInt(wk.WorkflowRunID, 10), filter.Search))) { continue }
        selected = append(selected, wk)
    }
    sort.SliceStable(selected, func(i, j int) bool {
        a, b := selected[i].UpdatedAt, selected[j].UpdatedAt
        if a.IsZero() { a = selected[i].CreatedAt }
        if b.IsZero() { b = selected[j].CreatedAt }
        if a.Equal(b) { return selected[i].ID > selected[j].ID }
        return a.After(b)
    })
    // The endpoint guarantees a stable, non-nil array even at the end.
    page := workerHistoryPage{Items: []core.Worker{},Total:len(selected),Limit:filter.Limit,Offset:filter.Offset}
    if filter.Offset >= len(selected) { return page }
    end := filter.Offset + filter.Limit
    if end > len(selected) { end = len(selected) }
    page.Items = selected[filter.Offset:end]
    page.HasMore = end < len(selected)
    return page
}


func workerMatchesHistoryState(wk core.Worker, requested string) bool {
    requested = strings.ToLower(strings.TrimSpace(requested))
    state := strings.ToLower(string(wk.State))
    result := strings.ToLower(strings.TrimSpace(wk.JobResult))
    if requested == state || requested == result { return true }
    switch requested {
    case "failed":
        return result == "failure" || (wk.FailureReason != "" && result == "")
    case "succeeded":
        return result == "success"
    case "completed":
        return result == "success" || result == "succeeded"
    case "canceled":
        return result == "cancelled"
    }
    return false
}
