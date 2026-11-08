package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
	"github.com/eutelic/runnerhub/internal/telemetry"
)

func TestScaleEventRecordsValidZeroSecondQueueSample(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worker := core.Worker{ID: "w-zero", PoolID: "p-zero", RunnerName: "runner-zero", State: core.WorkerReady}
	if err := db.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	instant := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"poolId":"p-zero","runnerName":"runner-zero","state":"started","queuedAt":%q,"startedAt":%q}`, instant, instant)
	metrics := &telemetry.Metrics{}
	svc := &Service{Store: db, Metrics: metrics}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	observed := metrics.PoolSnapshot("p-zero")
	if observed.QueueSamples != 1 || observed.QueueTotal != 0 {
		t.Fatalf("expected zero-second queue sample: %+v", observed)
	}
}
