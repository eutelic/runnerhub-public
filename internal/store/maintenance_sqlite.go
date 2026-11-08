//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"errors"
)

// GetMaintenance returns the persisted global scheduling pause state.
func (s *SQLite) GetMaintenance(ctx context.Context) (MaintenanceState, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM deployment_maintenance WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceState{}, nil
	}
	if err != nil {
		return MaintenanceState{}, err
	}
	return decode[MaintenanceState](raw)
}

// SetMaintenance updates the singleton row without affecting the worker ledger.
func (s *SQLite) SetMaintenance(ctx context.Context, state MaintenanceState) error {
	raw, err := marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deployment_maintenance(id,data) VALUES(1,?)
 ON CONFLICT(id) DO UPDATE SET data=excluded.data`, raw)
	return err
}
