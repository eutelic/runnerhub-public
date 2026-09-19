package telemetry

import (
	"fmt"
	"testing"
	"time"
)

func TestQueueMetricsArePoolSpecific(t *testing.T) {
	var m Metrics
	m.Queue("fast", 2*time.Second)
	m.Queue("fast", 4*time.Second)
	m.Queue("slow", 90*time.Second)

	fast := m.PoolSnapshot("fast")
	slow := m.PoolSnapshot("slow")
	if fast.QueueSamples != 2 || fast.QueueTotal != 6*time.Second {
		t.Fatalf("fast snapshot=%+v", fast)
	}
	if slow.QueueSamples != 1 || slow.QueueTotal != 90*time.Second {
		t.Fatalf("slow snapshot=%+v", slow)
	}

	global := m.Snapshot()
	if global.QueueSamples != 3 || global.QueueTotal != 96*time.Second {
		t.Fatalf("global snapshot=%+v", global)
	}
}

func TestQueueMetricsPoolTrackingIsBounded(t *testing.T) {
	var m Metrics
	for i := 0; i < maxTrackedPools+32; i++ {
		m.Queue(fmt.Sprintf("pool-%03d", i), time.Second)
	}
	if len(m.pools) != maxTrackedPools {
		t.Fatalf("tracked pools=%d, want %d", len(m.pools), maxTrackedPools)
	}
	if _, ok := m.pools["pool-000"]; ok {
		t.Fatal("oldest pool metric was not evicted")
	}
	if _, ok := m.pools[fmt.Sprintf("pool-%03d", maxTrackedPools+31)]; !ok {
		t.Fatal("newest pool metric missing")
	}
}
