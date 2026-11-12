//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/core"
	_ "modernc.org/sqlite"
	"time"
)

type SQLite struct{ db *sql.DB }

func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrateOperations(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrateAgentUpdateLeases(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *SQLite) Close() error { return s.db.Close() }
func (s *SQLite) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS hosts(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pools(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workers(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS fleet_alerts(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_workers_updated_history ON workers(updated_at DESC,id DESC);
-- Live control-plane paths must never scan historical deleted workers.
CREATE INDEX IF NOT EXISTS idx_workers_non_deleted ON workers(updated_at DESC,id DESC) WHERE COALESCE(json_extract(data,'$.state'),'') != 'deleted';
-- Include deleted rows so an old GitHub JIT runner cannot be created twice after restart.
CREATE INDEX IF NOT EXISTS idx_workers_scale_identity ON workers(json_extract(data,'$.poolId'),json_extract(data,'$.runnerName')) WHERE COALESCE(json_extract(data,'$.runnerName'),'') != '';
CREATE INDEX IF NOT EXISTS idx_workers_ci_candidates ON workers(julianday(CASE WHEN COALESCE(json_extract(data,'$.completedAt'),'') NOT IN ('','0001-01-01T00:00:00Z') THEN json_extract(data,'$.completedAt') ELSE json_extract(data,'$.failedAt') END)) WHERE COALESCE(json_extract(data,'$.repository'),'') != '' AND CAST(COALESCE(json_extract(data,'$.workflowRunId'),0) AS INTEGER)>0 AND COALESCE(json_extract(data,'$.startedAt'),'') != '' AND (CASE WHEN COALESCE(json_extract(data,'$.completedAt'),'') NOT IN ('','0001-01-01T00:00:00Z') THEN json_extract(data,'$.completedAt') ELSE json_extract(data,'$.failedAt') END) != '';
CREATE TABLE IF NOT EXISTS audit_events(id INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, result TEXT NOT NULL, safe_detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
-- Retain command IDs for durable, exactly-once result observation across retries/restarts.
CREATE TABLE IF NOT EXISTS provider_delete_results(command_id TEXT PRIMARY KEY, observed_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_provider_delete ON audit_events(id) WHERE action='provider_delete_result';
CREATE TABLE IF NOT EXISTS agent_commands(id TEXT PRIMARY KEY, host_id TEXT NOT NULL, worker_id TEXT NOT NULL, command_type TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL, lease_until TEXT, attempts INTEGER NOT NULL DEFAULT 0, acked_at TEXT);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_commands_active_semantic ON agent_commands(host_id, worker_id, command_type) WHERE acked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_commands_delivery ON agent_commands(host_id, acked_at, lease_until, created_at);
-- The global admission gate persists across self-redeployment.
CREATE TABLE IF NOT EXISTS deployment_maintenance(id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL);`)
	return err
}
func marshal(v any) ([]byte, error)     { return json.Marshal(v) }
func decode[T any](b []byte) (T, error) { var v T; err := json.Unmarshal(b, &v); return v, err }
func upsert(ctx context.Context, db *sql.DB, table, id string, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	q := fmt.Sprintf("INSERT INTO %s(id,data,updated_at) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", table)
	_, err = db.ExecContext(ctx, q, id, b, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func get[T any](ctx context.Context, db *sql.DB, table, id string) (T, error) {
	var b []byte
	q := fmt.Sprintf("SELECT data FROM %s WHERE id=?", table)
	err := db.QueryRowContext(ctx, q, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		var z T
		return z, ErrNotFound
	}
	if err != nil {
		var z T
		return z, err
	}
	return decode[T](b)
}
func list[T any](ctx context.Context, db *sql.DB, table string) ([]T, error) {
	q := fmt.Sprintf("SELECT data FROM %s ORDER BY updated_at DESC", table)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		v, err := decode[T](b)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLite) UpsertHost(ctx context.Context, v core.Host) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO hosts(id,data,updated_at) VALUES(?,?,?)
 ON CONFLICT(id) DO UPDATE SET
 data=json_set(excluded.data,'$.schedulingDisabled',json(CASE WHEN COALESCE(json_extract(hosts.data,'$.schedulingDisabled'),0)=1 THEN 'true' ELSE 'false' END)),
 updated_at=excluded.updated_at
 WHERE json_extract(hosts.data,'$.state') != 'archived'`, v.ID, b, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrHostArchived
	}
	return nil
}
func (s *SQLite) GetHost(ctx context.Context, id string) (core.Host, error) {
	return get[core.Host](ctx, s.db, "hosts", id)
}
func (s *SQLite) ListHosts(ctx context.Context) ([]core.Host, error) {
	return list[core.Host](ctx, s.db, "hosts")
}
func (s *SQLite) UpsertPool(ctx context.Context, v core.RunnerPool) error {
	return upsert(ctx, s.db, "pools", v.ID, v)
}
func (s *SQLite) GetPool(ctx context.Context, id string) (core.RunnerPool, error) {
	return get[core.RunnerPool](ctx, s.db, "pools", id)
}
func (s *SQLite) ListPools(ctx context.Context) ([]core.RunnerPool, error) {
	return list[core.RunnerPool](ctx, s.db, "pools")
}
func (s *SQLite) UpsertWorker(ctx context.Context, v core.Worker) error {
	return upsert(ctx, s.db, "workers", v.ID, v)
}
func (s *SQLite) GetWorker(ctx context.Context, id string) (core.Worker, error) {
	return get[core.Worker](ctx, s.db, "workers", id)
}
func (s *SQLite) ListWorkers(ctx context.Context) ([]core.Worker, error) {
	return list[core.Worker](ctx, s.db, "workers")
}

const nonDeletedWorkersQuery = "SELECT data FROM workers WHERE COALESCE(json_extract(data,'$.state'),'') != 'deleted' ORDER BY updated_at DESC,id DESC"

// ListNonDeletedWorkers is the indexed snapshot for live reconciliation and
// scale-set recovery. ListWorkers still includes the full historical ledger.
func (s *SQLite) ListNonDeletedWorkers(ctx context.Context) ([]core.Worker, error) {
	rows, err := s.db.QueryContext(ctx, nonDeletedWorkersQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Worker{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		worker, err := decode[core.Worker](raw)
		if err != nil {
			return nil, err
		}
		out = append(out, worker)
	}
	return out, rows.Err()
}
func (s *SQLite) AppendAudit(ctx context.Context, e core.AuditEvent) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO audit_events(actor,action,resource_type,resource_id,result,safe_detail,created_at) VALUES(?,?,?,?,?,?,?)", e.Actor, e.Action, e.ResourceType, e.ResourceID, e.Result, e.SafeDetail, e.CreatedAt.Format(time.RFC3339Nano))
	return err
}
func (s *SQLite) ListAudit(ctx context.Context, limit int) ([]core.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,actor,action,resource_type,resource_id,result,safe_detail,created_at FROM audit_events ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.AuditEvent
	for rows.Next() {
		var e core.AuditEvent
		var created string
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.ResourceType, &e.ResourceID, &e.Result, &e.SafeDetail, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *SQLite) EnqueueCommand(ctx context.Context, hostID string, c agent.Command) error {
	if hostID == "" || c.ID == "" || c.WorkerID == "" || c.Type == "" {
		return errors.New("command host, id, worker and type are required")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_commands(id,host_id,worker_id,command_type,payload,created_at) VALUES(?,?,?,?,?,?)`, c.ID, hostID, c.WorkerID, string(c.Type), b, c.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *SQLite) LeaseCommands(ctx context.Context, hostID string, now, leaseUntil time.Time, limit int) ([]agent.Command, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM agent_commands WHERE host_id=? AND acked_at IS NULL AND (lease_until IS NULL OR lease_until<=?) ORDER BY created_at,id LIMIT ?`, hostID, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	type leased struct {
		id  string
		cmd agent.Command
	}
	var got []leased
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		var c agent.Command
		if err := json.Unmarshal(payload, &c); err != nil {
			rows.Close()
			return nil, err
		}
		got = append(got, leased{id: id, cmd: c})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, item := range got {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_commands SET lease_until=?,attempts=attempts+1 WHERE id=? AND acked_at IS NULL`, leaseUntil.UTC().Format(time.RFC3339Nano), item.id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]agent.Command, 0, len(got))
	for _, item := range got {
		out = append(out, item.cmd)
	}
	return out, nil
}
func (s *SQLite) GetCommand(ctx context.Context, id string) (CommandRecord, error) {
	var hostID, workerID, typ string
	var payload []byte
	var lease, ack sql.NullString
	var attempts int
	err := s.db.QueryRowContext(ctx, `SELECT host_id,worker_id,command_type,payload,lease_until,attempts,acked_at FROM agent_commands WHERE id=?`, id).Scan(&hostID, &workerID, &typ, &payload, &lease, &attempts, &ack)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, ErrNotFound
	}
	if err != nil {
		return CommandRecord{}, err
	}
	c := agent.Command{ID: id, WorkerID: workerID, Type: agent.CommandType(typ)}
	if len(payload) > 0 {
		var stored agent.Command
		if json.Unmarshal(payload, &stored) == nil {
			c = stored
			c.ID = id
			c.WorkerID = workerID
			c.Type = agent.CommandType(typ)
		}
	}
	rec := CommandRecord{HostID: hostID, Command: c, Attempts: attempts}
	if lease.Valid {
		rec.LeaseUntil, _ = time.Parse(time.RFC3339Nano, lease.String)
	}
	if ack.Valid {
		rec.AckedAt, _ = time.Parse(time.RFC3339Nano, ack.String)
	}
	return rec, nil
}
func (s *SQLite) AckCommand(ctx context.Context, id string, at time.Time) error {
	stamp := at.UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `UPDATE agent_commands SET acked_at=?,lease_until=NULL,payload='{}' WHERE id=? AND acked_at IS NULL`, stamp, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var ack sql.NullString
		err := s.db.QueryRowContext(ctx, "SELECT acked_at FROM agent_commands WHERE id=?", id).Scan(&ack)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	cutoff := at.Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM agent_commands WHERE acked_at IS NOT NULL AND acked_at<?`, cutoff)
	return nil
}

// RecordProviderDeleteResult claims a delete command exactly once and inserts
// its audit event in the same SQLite transaction. A post-callback ACK failure
// cannot create duplicate audit evidence on redelivery.
func (s *SQLite) RecordProviderDeleteResult(ctx context.Context, commandID, hostID, workerID string, event core.AuditEvent) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	at := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO provider_delete_results(command_id,observed_at)
SELECT id,? FROM agent_commands WHERE id=? AND host_id=? AND worker_id=? AND command_type=?`, at.Format(time.RFC3339Nano), commandID, hostID, workerID, string(agent.CommandDelete))
	if err != nil {
		return false, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		return false, nil
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = at
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO audit_events(actor,action,resource_type,resource_id,result,safe_detail,created_at) VALUES(?,?,?,?,?,?,?)`,
		event.Actor, event.Action, event.ResourceType, event.ResourceID, event.Result, event.SafeDetail, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLite) ProviderDeleteSummary(ctx context.Context) (ProviderDeleteSummary, error) {
	var summary ProviderDeleteSummary
	rows, err := s.db.QueryContext(ctx, `SELECT result,safe_detail,created_at FROM audit_events WHERE action='provider_delete_result' ORDER BY id`)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var result, detail, createdAt string
		if err := rows.Scan(&result, &detail, &createdAt); err != nil {
			return summary, err
		}
		observedAt, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return summary, err
		}
		summary.Reports++
		if result == "agent_reported_failure" {
			summary.Failures++
		}
		if summary.FirstObservedAt == nil || observedAt.Before(*summary.FirstObservedAt) {
			summary.FirstObservedAt = &observedAt
		}
		if summary.LastObservedAt == nil || observedAt.After(*summary.LastObservedAt) {
			summary.LastObservedAt = &observedAt
		}
		if durationNanos := providerDeleteDuration(detail); durationNanos > 0 {
			summary.Measured++
			if durationNanos > int64(^uint64(0)>>1)-summary.DurationTotalNanos {
				summary.DurationTotalNanos = int64(^uint64(0) >> 1)
			} else {
				summary.DurationTotalNanos += durationNanos
			}
		}
	}
	if err := rows.Err(); err != nil {
		return summary, err
	}
	summary.Scope = "durable-audit-ledger"
	summary.PhysicalReclamation = false
	return summary, nil
}
