package cireconcile

import (
	"sync"
	"time"
)

// DefaultAnalyticsLookback matches RunnerHub's default CI Efficiency dashboard
// window so deterministic findings are not silently drawn from a much shorter
// period than the execution totals they accompany.
const DefaultAnalyticsLookback = 30 * 24 * time.Hour

type StatusSnapshot struct {
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
	Lookback      time.Duration
}

type Status struct {
	mu       sync.RWMutex
	snapshot StatusSnapshot
}

func NewStatus(lookback time.Duration) *Status {
	if lookback <= 0 {
		lookback = DefaultAnalyticsLookback
	}
	return &Status{snapshot: StatusSnapshot{Lookback: lookback}}
}

func (s *Status) Snapshot() StatusSnapshot {
	if s == nil {
		return StatusSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Status) begin(at time.Time, lookback time.Duration) {
	if s == nil {
		return
	}
	if lookback <= 0 {
		lookback = DefaultAnalyticsLookback
	}
	s.mu.Lock()
	s.snapshot.LastAttemptAt = at.UTC()
	s.snapshot.Lookback = lookback
	s.mu.Unlock()
}

func (s *Status) finish(at time.Time, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// Detailed reconciliation errors remain in the sanitized audit stream.
		// The dashboard only needs a safe health signal.
		s.snapshot.LastError = "CI metadata reconciliation failed; see audit log"
		return
	}
	s.snapshot.LastSuccessAt = at.UTC()
	s.snapshot.LastError = ""
}
