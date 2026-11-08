package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

// Archiving is deliberately non-destructive: workers, audit and command history stay intact.
var ErrHostArchived = errors.New("archived host cannot be updated")
var ErrHostNotArchivable = errors.New("host cannot be archived safely")

type HostArchiver interface {
	ArchiveHost(context.Context, string, string, time.Time) (core.Host, error)
}

func validateHostArchive(host core.Host, now time.Time) error {
	if host.State != core.HostOffline {
		return fmt.Errorf("%w: host must be offline (current state: %s)", ErrHostNotArchivable, host.State)
	}
	if host.LastSeenAt.IsZero() || now.Sub(host.LastSeenAt) < 5*time.Minute {
		return fmt.Errorf("%w: last heartbeat must be known and at least five minutes old", ErrHostNotArchivable)
	}
	return nil
}

func validateMergeTarget(old, target core.Host, now time.Time) error {
	if old.ID == target.ID {
		return fmt.Errorf("%w: cannot merge host into itself", ErrHostNotArchivable)
	}
	if target.State != core.HostOnline {
		return fmt.Errorf("%w: merge target must be online", ErrHostNotArchivable)
	}
	if target.LastSeenAt.IsZero() || target.LastSeenAt.After(now.Add(time.Minute)) || now.Sub(target.LastSeenAt) > 2*time.Minute {
		return fmt.Errorf("%w: target must have a recent verified heartbeat", ErrHostNotArchivable)
	}
	if !strings.EqualFold(strings.TrimSpace(old.Name), strings.TrimSpace(target.Name)) || old.Provider != target.Provider {
		return fmt.Errorf("%w: merge target must have the same recorded name and provider", ErrHostNotArchivable)
	}
	return nil
}
