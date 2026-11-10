//go:build !localtest

package store

import (
	"context"
	"errors"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func (s *SQLite) SetHostSchedulingDisabled(ctx context.Context, id string, disabled bool) (core.Host, error) {
	value := "false"
	if disabled {
		value = "true"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET
        data=json_set(data,'$.schedulingDisabled',json(?)),updated_at=?
        WHERE id=? AND json_extract(data,'$.state')!='archived'`,
		value, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return core.Host{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return core.Host{}, err
	}
	if n == 0 {
		host, err := s.GetHost(ctx, id)
		if err != nil {
			return core.Host{}, err
		}
		if host.State == core.HostArchived {
			return core.Host{}, ErrHostArchived
		}
		return core.Host{}, errors.New("host scheduling update did not change any row")
	}
	return s.GetHost(ctx, id)
}
