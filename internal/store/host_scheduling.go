package store

import (
	"context"

	"github.com/eutelic/runnerhub/internal/core"
)

// HostSchedulingSetter atomically changes host eligibility while preserving heartbeats.
type HostSchedulingSetter interface {
	SetHostSchedulingDisabled(context.Context, string, bool) (core.Host, error)
}
