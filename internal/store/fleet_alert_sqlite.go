//go:build !localtest

package store

import "context"

func (s *SQLite) UpsertFleetAlert(ctx context.Context, alert FleetAlert) error {
	return upsert(ctx, s.db, "fleet_alerts", alert.ID, alert)
}

func (s *SQLite) GetFleetAlert(ctx context.Context, id string) (FleetAlert, error) {
	return get[FleetAlert](ctx, s.db, "fleet_alerts", id)
}

func (s *SQLite) ListFleetAlerts(ctx context.Context) ([]FleetAlert, error) {
	return s.listFleetAlerts(ctx, `SELECT data FROM fleet_alerts ORDER BY updated_at DESC,id LIMIT 200`)
}

func (s *SQLite) ListOpenFleetAlerts(ctx context.Context) ([]FleetAlert, error) {
	return s.listFleetAlerts(ctx, `SELECT data FROM fleet_alerts WHERE json_extract(data,'$.state') IN ('observing','active','resolving') ORDER BY updated_at DESC,id LIMIT 1000`)
}

func (s *SQLite) listFleetAlerts(ctx context.Context, query string) ([]FleetAlert, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FleetAlert, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		alert, err := decode[FleetAlert](data)
		if err != nil {
			return nil, err
		}
		out = append(out, alert)
	}
	return out, rows.Err()
}
