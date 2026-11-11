package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capacityUpdates struct {
	mu     sync.Mutex
	values []int
	signal chan int
}

func (c *capacityUpdates) SetMaxRunners(n int) {
	c.mu.Lock()
	c.values = append(c.values, n)
	c.mu.Unlock()
	select {
	case c.signal <- n:
	default:
	}
}

func TestAdvertisedCapacityRequiresControllerEvidence(t *testing.T) {
	var response atomic.Value
	response.Store(`{"healthy":true,"capacityLimit":2}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pools/other/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, response.Load().(string))
	}))
	defer srv.Close()
	hub := runnerHubClient{base: srv.URL, poolID: "other", http: srv.Client()}

	got, err := hub.advertisedCapacity(context.Background(), 10)
	if err != nil || got != 2 {
		t.Fatalf("physical cap=%d, err=%v", got, err)
	}
	got, err = hub.advertisedCapacity(context.Background(), 1)
	if err != nil || got != 1 {
		t.Fatalf("pool cap=%d, err=%v", got, err)
	}
	response.Store(`{"healthy":false,"capacityLimit":0}`)
	got, err = hub.advertisedCapacity(context.Background(), 10)
	if err != nil || got != 0 {
		t.Fatalf("zero-capacity host=%d, err=%v", got, err)
	}
	response.Store(`{"healthy":true}`)
	if _, err := hub.advertisedCapacity(context.Background(), 10); err == nil ||
		!strings.Contains(err.Error(), "update the control plane") {
		t.Fatalf("missing capacity field should fail closed: %v", err)
	}
	response.Store(`{"healthy":true,"capacityLimit":-1}`)
	if _, err := hub.advertisedCapacity(context.Background(), 10); err == nil ||
		!strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative capacity should fail closed: %v", err)
	}
}

func TestCapacityRefreshUpdatesWithoutSessionRestart(t *testing.T) {
	var current atomic.Int64
	current.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"healthy":true,"capacityLimit":%d}`, current.Load())
	}))
	defer srv.Close()
	hub := runnerHubClient{base: srv.URL, poolID: "pool", http: srv.Client()}
	updates := &capacityUpdates{signal: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshAdvertisedCapacity(ctx, hub, 4, updates, 1, 5*time.Millisecond)
	}()

	current.Store(3)
	select {
	case observed := <-updates.signal:
		if observed != 3 {
			t.Fatalf("refreshed capacity=%d, want 3", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not raise capacity after host became available")
	}
	current.Store(0)
	select {
	case observed := <-updates.signal:
		if observed != 0 {
			t.Fatalf("reduced capacity=%d, want 0", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not remove unavailable capacity")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity reconciliation did not stop on cancellation")
	}
}

func TestCapacityRefreshFailsClosedAndRecovers(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"healthy":true,"capacityLimit":3}`)
	}))
	defer srv.Close()
	hub := runnerHubClient{base: srv.URL, poolID: "pool", http: srv.Client()}
	updates := &capacityUpdates{signal: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshAdvertisedCapacity(ctx, hub, 5, updates, 2, 5*time.Millisecond)
	}()

	select {
	case observed := <-updates.signal:
		if observed != 0 {
			t.Fatalf("unavailable control plane retained capacity %d, want zero", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("failed capacity refresh did not withdraw stale advertised capacity")
	}

	unavailable.Store(false)
	select {
	case observed := <-updates.signal:
		if observed != 3 {
			t.Fatalf("recovered capacity=%d, want 3", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh controller evidence did not restore advertised capacity")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity reconciliation did not stop on cancellation")
	}
}
