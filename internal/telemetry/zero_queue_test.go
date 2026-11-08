package telemetry

import (
	"testing"
	"time"
)

func TestQueueMetricsIncludeMeasuredZeroAndRejectNegative(t *testing.T) {
	var m Metrics
	m.Queue("p", 0)
	m.Queue("p", -time.Second)
	got := m.PoolSnapshot("p")
	if got.QueueSamples != 1 || got.QueueTotal != 0 {
		t.Fatalf("zero wait sample was dropped, or negative wait was counted: %+v", got)
	}
	global := m.Snapshot()
	if global.QueueSamples != 1 || global.QueueTotal != 0 {
		t.Fatalf("incorrect global zero wait coverage: %+v", global)
	}
}
