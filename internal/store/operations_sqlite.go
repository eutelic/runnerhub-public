//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const operationActive = "('queued','working','waiting')"

func (s *SQLite) migrateOperations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS operations(
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	target TEXT NOT NULL,
	state TEXT NOT NULL,
	data BLOB NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS operations_one_active_target
	ON operations(kind,target) WHERE state IN ('queued','working','waiting');
CREATE INDEX IF NOT EXISTS operations_recent ON operations(updated_at DESC);
`)
	if err != nil {
		return err
	}
	return s.markInterruptedOnStartup(ctx)
}

// A process restart loses the in-memory SSH subprocess and its credentials.
// Recovered operations cannot truthfully remain "working"; their remote
// outcome must be checked before another enrollment is attempted.
func (s *SQLite) markInterruptedOnStartup(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM operations WHERE state IN ('queued','working','waiting')`)
	if err != nil {
		return err
	}
	var active []Operation
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var op Operation
		if err := json.Unmarshal(raw, &op); err != nil {
			rows.Close()
			return err
		}
		active = append(active, op)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, op := range active {
		now := time.Now().UTC()
		op.State = "unknown"
		op.Step = "server-restarted"
		op.Detail = "The control plane restarted during enrollment. Verify the remote agent before retrying."
		op.UpdatedAt = now
		op.EndedAt = &now
		op.Events = append(op.Events, OperationEvent{State: "unknown", Step: op.Step, At: now})
		raw, err := json.Marshal(op)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE operations SET state='unknown',data=?,updated_at=? WHERE id=? AND state IN ('queued','working','waiting')`,
			raw, now.Format(time.RFC3339Nano), op.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) expireInterruptedOperations(ctx context.Context) error {
	// The bootstrap worker has an eight-minute hard timeout. Only after twelve
	// minutes without a state transition can a preexisting operation be marked
	// unknown, allowing an operator to investigate before another attempt.
	cutoff := time.Now().UTC().Add(-12 * time.Minute).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM operations WHERE state IN ('queued','working','waiting') AND updated_at < ?`, cutoff)
	if err != nil {
		return err
	}
	var stale []Operation
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var op Operation
		if err := json.Unmarshal(raw, &op); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, op)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, op := range stale {
		op.State = "unknown"
		op.Detail = "No progress was recorded before the server-side operation expired. Verify the host before retrying."
		op.Step = "outcome-unknown"
		now := time.Now().UTC()
		op.UpdatedAt = now
		op.EndedAt = &now
		op.Events = append(op.Events, OperationEvent{State: op.State, Step: op.Step, At: now})
		raw, err := json.Marshal(op)
		if err != nil {
			return err
		}
		// Conditional update avoids overwriting a concurrent active stage.
		_, err = s.db.ExecContext(ctx, `
UPDATE operations SET state='unknown',data=?,updated_at=?
WHERE id=? AND state IN ('queued','working','waiting') AND updated_at < ?`,
			raw, now.Format(time.RFC3339Nano), op.ID, cutoff)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) StartOperation(ctx context.Context, op Operation) (Operation, bool, error) {
	if err := s.expireInterruptedOperations(ctx); err != nil {
		return Operation{}, false, err
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return Operation{}, false, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO operations(id,kind,target,state,data,updated_at) VALUES(?,?,?,?,?,?)`,
		op.ID, op.Kind, op.Target, op.State, raw, op.UpdatedAt.Format(time.RFC3339Nano))
	if err == nil {
		return op, false, nil
	}
	// A unique active-target collision means an enrollment already started.
	var current []byte
	lookupErr := s.db.QueryRowContext(ctx, `
SELECT data FROM operations WHERE kind=? AND target=? AND state IN ('queued','working','waiting')`,
		op.Kind, op.Target).Scan(&current)
	if lookupErr == nil {
		var existing Operation
		if decodeErr := json.Unmarshal(current, &existing); decodeErr != nil {
			return Operation{}, false, decodeErr
		}
		return existing, true, nil
	}
	return Operation{}, false, err
}

func (s *SQLite) GetOperation(ctx context.Context, id string) (Operation, error) {
	if err := s.expireInterruptedOperations(ctx); err != nil {
		return Operation{}, err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM operations WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	err = json.Unmarshal(raw, &op)
	return op, err
}

func (s *SQLite) ListOperations(ctx context.Context, limit int) ([]Operation, error) {
	if err := s.expireInterruptedOperations(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT data FROM operations ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []Operation{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var op Operation
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

func (s *SQLite) UpdateOperation(ctx context.Context, op Operation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE operations SET state=?,data=?,updated_at=? WHERE id=?`,
		op.State, raw, op.UpdatedAt.Format(time.RFC3339Nano), op.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
