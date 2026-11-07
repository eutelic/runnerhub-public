package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// The listener advertises an upper bound, never the pool's theoretical
// concurrency when there is no verified physical host capacity.
func (c runnerHubClient) advertisedCapacity(ctx context.Context, configuredMax int) (int, error) {
	var health poolHealthResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/pools/"+c.poolID+"/health", nil, &health); err != nil {
		return 0, err
	}
	if health.CapacityLimit == nil {
		return 0, errors.New("RunnerHub controller has no capacityLimit field; update the control plane first")
	}
	if *health.CapacityLimit < 0 {
		return 0, fmt.Errorf("invalid negative capacityLimit for pool %q", c.poolID)
	}
	if configuredMax < 0 {
		return 0, errors.New("configured max runners must not be negative")
	}
	return min(*health.CapacityLimit, configuredMax), nil
}

type runnerCapacitySetter interface {
	SetMaxRunners(int)
}

// The SDK makes SetMaxRunners concurrently safe with listener.Run. Polling is
// independent of GitHub demand messages, so an offline/drained host becoming
// eligible can raise the advertised capacity without restarting the session.
// A failed refresh retains the last verified bound; host admission still
// independently rejects stale capacity and no new unlimited value is used.
func refreshAdvertisedCapacity(ctx context.Context, hub runnerHubClient, max int,
	listener runnerCapacitySetter, initial int, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	last := initial
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := hub.advertisedCapacity(ctx, max)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("refresh scale-set capacity", "pool", hub.poolID, "error", err)
				continue
			}
			if current != last {
				listener.SetMaxRunners(current)
				slog.Info("updated GitHub scale-set capacity", "pool", hub.poolID,
					"previous", last, "current", current)
				last = current
			}
		}
	}
}
