package store

import (
	"context"
	"time"
)

// FleetAlert is a durable observation of one stable fleet condition.
type FleetAlert struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Detail            string    `json:"detail"`
	Href              string    `json:"href"`
	Severity          string    `json:"severity"`
	Cause             string    `json:"cause"`
	AffectedType      string    `json:"affectedType,omitempty"`
	AffectedID        string    `json:"affectedId,omitempty"`
	Repository        string    `json:"repository,omitempty"`
	State             string    `json:"state"`
	FirstSeenAt       time.Time `json:"firstSeenAt"`
	LastSeenAt        time.Time `json:"lastSeenAt"`
	RecoveryStartedAt time.Time `json:"recoveryStartedAt,omitempty"`
	ResolvedAt        time.Time `json:"resolvedAt,omitempty"`
	AcknowledgedAt    time.Time `json:"acknowledgedAt,omitempty"`
	MutedUntil        time.Time `json:"mutedUntil,omitempty"`
}

// FleetAlertStore is optional so lightweight Store implementations used by
// tests and tools do not have to persist alert state.
type FleetAlertStore interface {
	UpsertFleetAlert(context.Context, FleetAlert) error
	GetFleetAlert(context.Context, string) (FleetAlert, error)
	ListFleetAlerts(context.Context) ([]FleetAlert, error)
	ListOpenFleetAlerts(context.Context) ([]FleetAlert, error)
}
