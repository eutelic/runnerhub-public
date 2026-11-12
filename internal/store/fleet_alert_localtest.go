//go:build localtest

package store

import (
	"context"
	"sort"
)

func (s *SQLite) UpsertFleetAlert(_ context.Context, alert FleetAlert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fleetAlerts == nil {
		s.fleetAlerts = map[string]FleetAlert{}
	}
	s.fleetAlerts[alert.ID] = alert
	return nil
}

func (s *SQLite) GetFleetAlert(_ context.Context, id string) (FleetAlert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alert, ok := s.fleetAlerts[id]
	if !ok {
		return FleetAlert{}, ErrNotFound
	}
	return alert, nil
}

func (s *SQLite) ListFleetAlerts(context.Context) ([]FleetAlert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FleetAlert, 0, len(s.fleetAlerts))
	for _, alert := range s.fleetAlerts {
		out = append(out, alert)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}

func (s *SQLite) ListOpenFleetAlerts(context.Context) ([]FleetAlert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []FleetAlert
	for _, alert := range s.fleetAlerts {
		if alert.State == "observing" || alert.State == "active" || alert.State == "resolving" {
			out = append(out, alert)
		}
	}
	return out, nil
}
