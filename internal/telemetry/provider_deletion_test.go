package telemetry

import (
    "testing"
    "time"
)

func TestProviderDeleteMetricsAreProcessLocalAndDurationSamplesOptional(t *testing.T) {
    m := &Metrics{}
    m.ProviderDeletion(true,0)
    m.ProviderDeletion(false,-time.Second)
    m.ProviderDeletion(true,3*time.Second)
    m.ProviderDeletion(false,25*time.Hour)
    s := m.Snapshot()
    if s.ProviderDeleteAttempts!=4 || s.ProviderDeleteFailures!=2 ||
        s.ProviderDeleteMeasured!=1 || s.ProviderDeleteDurationTotal!=3*time.Second {
        t.Fatalf("metrics=%+v",s)
    }
    if s.Scope!="process-local-volatile" {
        t.Fatalf("scope=%q, want process-local-volatile",s.Scope)
    }
    if s.CleanupTotal!=0 {
        t.Fatalf("provider deletion durations must not pollute historical lifecycle cleanupTotal: %v",s.CleanupTotal)
    }
}
