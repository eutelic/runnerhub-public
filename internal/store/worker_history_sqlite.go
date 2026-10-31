//go:build !localtest

package store

import (
    "context"
    "database/sql"
    "fmt"
    "strings"
    "github.com/eutelic/runnerhub/internal/core"
)

// QueryWorkerHistory applies filters, counts and LIMIT/OFFSET in SQLite. It
// decodes only the requested page instead of materializing the entire ledger.
// A read transaction keeps the count and rows on a consistent snapshot.
func (s *SQLite) QueryWorkerHistory(ctx context.Context, f WorkerHistoryQuery) (WorkerHistoryPage, error) {
    if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 || f.Offset > 100000 {
        return WorkerHistoryPage{}, fmt.Errorf("invalid worker history pagination")
    }
    const repository = `LOWER(COALESCE(NULLIF(json_extract(data,'$.repository'),''),
       CASE WHEN COALESCE(json_extract(data,'$.scope.owner'),'')<>'' AND
                 COALESCE(json_extract(data,'$.scope.repository'),'')<>''
            THEN json_extract(data,'$.scope.owner') || '/' ||
                 json_extract(data,'$.scope.repository') END, ''))`
    const state = `LOWER(COALESCE(json_extract(data,'$.state'),''))`
    const result = `LOWER(COALESCE(json_extract(data,'$.jobResult'),''))`
    const failure = `COALESCE(json_extract(data,'$.failureReason'),'')`
    conditions := []string{}
    args := []any{}
    if v := strings.ToLower(strings.TrimSpace(f.Repository)); v != "" {
        conditions = append(conditions, repository+"=?")
        args = append(args,v)
    }
    if v := strings.TrimSpace(f.Pool); v != "" {
        conditions = append(conditions, "json_extract(data,'$.poolId')=?")
        args = append(args,v)
    }
    if v := strings.TrimSpace(f.Host); v != "" {
        conditions = append(conditions, "json_extract(data,'$.hostId')=?")
        args = append(args,v)
    }
    if v := strings.ToLower(strings.TrimSpace(f.State)); v != "" {
        switch v {
        case "failed":
            conditions = append(conditions, "(" + state + "='failed' OR " + result +
                " IN ('failure','failed') OR (trim(" + failure + ")<>'' AND " + result + "=''))")
        case "succeeded":
            conditions = append(conditions, "(" + state + "='succeeded' OR " + result +
                " IN ('success','succeeded'))")
        case "completed":
            conditions = append(conditions, "(" + state + "='completed' OR " + result +
                " IN ('success','succeeded'))")
        case "canceled":
            conditions = append(conditions, "(" + state + "='canceled' OR " + result +
                " IN ('cancelled','canceled'))")
        default:
            conditions = append(conditions, "(" + state + "=? OR " + result + "=?)")
            args = append(args,v,v)
        }
    }
    if v := strings.ToLower(strings.TrimSpace(f.Search)); v != "" {
        like := "%" + escapeHistoryLike(v) + "%"
        // Treat the search as plain text, not a SQL LIKE pattern. An absent
        // workflowRunId must not cause a search for "0" to match every worker.
        const likeClause = ` LIKE ? ESCAPE '\'`
        parts := []string{
            repository + likeClause,
            `LOWER(COALESCE(json_extract(data,'$.jobName'),''))` + likeClause,
            `LOWER(COALESCE(json_extract(data,'$.failureReason'),''))` + likeClause,
            `LOWER(COALESCE(json_extract(data,'$.id'),''))` + likeClause,
            `LOWER(COALESCE(json_extract(data,'$.runnerName'),''))` + likeClause,
            `(COALESCE(json_extract(data,'$.workflowRunId'),0)>0 AND
                 CAST(json_extract(data,'$.workflowRunId') AS TEXT)` + likeClause + `)`,
        }
        conditions = append(conditions,"("+strings.Join(parts," OR ")+")")
        for range parts { args = append(args,like) }
    }
    where := ""
    if len(conditions)>0 { where = " WHERE "+strings.Join(conditions," AND ") }
    tx,err := s.db.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
    if err != nil { return WorkerHistoryPage{},err }
    defer tx.Rollback()
    page := WorkerHistoryPage{Items:[]core.Worker{},Limit:f.Limit,Offset:f.Offset}
    if err := tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM workers"+where,args...).Scan(&page.Total);err!=nil {
        return WorkerHistoryPage{},err
    }
    if page.Total <= f.Offset {
        if err := tx.Commit();err!=nil {return WorkerHistoryPage{},err}
        return page,nil
    }
    query := "SELECT data FROM workers" + where +
        " ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?"
    dataArgs := append(append([]any(nil),args...),f.Limit,f.Offset)
    rows,err := tx.QueryContext(ctx,query,dataArgs...)
    if err != nil {return WorkerHistoryPage{},err}
    for rows.Next() {
        var data []byte
        if err := rows.Scan(&data);err!=nil {rows.Close();return WorkerHistoryPage{},err}
        worker,err := decode[core.Worker](data)
        if err!=nil {rows.Close();return WorkerHistoryPage{},err}
        page.Items=append(page.Items,worker)
    }
    rowErr:=rows.Err()
    closeErr:=rows.Close()
    if rowErr!=nil {return WorkerHistoryPage{},rowErr}
    if closeErr!=nil {return WorkerHistoryPage{},closeErr}
    if err:=tx.Commit();err!=nil {return WorkerHistoryPage{},err}
    page.HasMore=f.Offset+len(page.Items)<page.Total
    return page,nil
}

func escapeHistoryLike(v string) string {
    replacer:=strings.NewReplacer("\\","\\\\","%","\\%","_","\\_")
    return replacer.Replace(v)
}
