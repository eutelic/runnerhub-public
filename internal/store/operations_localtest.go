//go:build localtest

package store

import (
	"context"
	"sort"
	"time"
)

func (s *SQLite) StartOperation(_ context.Context, op Operation) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.operations {
		if existing.Kind == op.Kind && existing.Target == op.Target &&
			(existing.State == "queued" || existing.State == "working" || existing.State == "waiting") {
			if existing.UpdatedAt.Before(time.Now().Add(-12 * time.Minute)) {
				existing.State = "unknown"
				s.operations[existing.ID] = existing
				continue
			}
			return existing, true, nil
		}
	}
	s.operations[op.ID] = op
	return op, false, nil
}
func (s *SQLite) GetOperation(_ context.Context, id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return op, nil
}
func (s *SQLite) ListOperations(_ context.Context, limit int) ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 || limit > 100 {
		limit = 25
	}
	result := make([]Operation, 0, len(s.operations))
	for _, op := range s.operations {
		result = append(result, op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (s *SQLite) UpdateOperation(_ context.Context, op Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.operations[op.ID]; !ok {
		return ErrNotFound
	}
	s.operations[op.ID] = op
	return nil
}
