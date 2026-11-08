package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
	"github.com/eutelic/runnerhub/internal/telemetry"
	"strings"
	"time"
)

func (s *Service) HandleAgentResult(ctx context.Context, hostID string, res agent.CommandResult) error {
	if rec, err := s.Store.GetCommand(ctx, res.CommandID); err == nil && rec.Command.Type == agent.CommandUpdateAgent {
		return s.handleAgentUpdaterLaunchResult(ctx, rec, hostID, res)
	}
	wk, err := s.Store.GetWorker(ctx, res.WorkerID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	wk.UpdatedAt = now
	if !res.OK {
		wk.State = core.WorkerFailed
		wk.FailureReason = truncate(res.SafeError)
		wk.FailedAt = now
		_ = s.Store.AppendAudit(ctx, core.AuditEvent{Actor: "agent:" + hostID, Action: "command_result", ResourceType: "worker", ResourceID: wk.ID, Result: "error", SafeDetail: wk.FailureReason})
		if err := s.Store.UpsertWorker(ctx, wk); err != nil {
			return err
		}
		if err := s.recordProviderDeleteResult(ctx, hostID, res); err != nil {
			return err
		}
		return s.enqueueTerminalCleanup(ctx, &wk)
	}
	if res.Handle != nil {
		wk.ProviderHandle = res.Handle.ProviderID
		if core.CanTransitionWorker(wk.State, core.WorkerStarting) {
			wk.State = core.WorkerStarting
		}
		if core.CanTransitionWorker(wk.State, core.WorkerRegistering) {
			wk.State = core.WorkerRegistering
		}
		if wk.State == core.WorkerRegistering && wk.RegistrationStartedAt.IsZero() {
			wk.RegistrationStartedAt = now
		}
	}
	terminal := false
	if res.State != nil {
		terminalSample := res.State.State == core.WorkerCompleted || res.State.State == core.WorkerFailed
		recordWorkerUsage(&wk, res.State.Usage, now, (wk.State == core.WorkerBusy && wk.CompletedAt.IsZero()) || (terminalSample && !wk.StartedAt.IsZero()))
		preJob := wk.StartedAt.IsZero() && wk.State != core.WorkerBusy && wk.State != core.WorkerCompleted
		wk.ProviderExitCode = res.State.ExitCode
		wk.ProviderOOMKilled = res.State.OOMKilled
		if preJob && strings.TrimSpace(res.State.Diagnostics) != "" {
			wk.ProviderDiagnostics = res.State.Diagnostics
		}
		switch res.State.State {
		case core.WorkerCompleted:
			if core.CanTransitionWorker(wk.State, core.WorkerCompleted) {
				wk.State = core.WorkerCompleted
				if wk.CompletedAt.IsZero() {
					wk.CompletedAt = now
				}
			} else if wk.State != core.WorkerCompleted {
				wk.State = core.WorkerFailed
				wk.FailureReason = "provider exited before worker reached a runnable state"
				wk.FailedAt = now
			}
			terminal = true
		case core.WorkerFailed:
			wk.State = core.WorkerFailed
			wk.FailureReason = truncate(res.State.SafeDetail)
			wk.FailedAt = now
			if res.State.ExitCode != nil {
				wk.FailureReason = truncate(fmt.Sprintf("%s (exit code %d)", wk.FailureReason, *res.State.ExitCode))
			}
			terminal = true
		case core.WorkerDeleted:
			if wk.State == core.WorkerCleanupPending || wk.State == core.WorkerDeleting {
				wk.State = core.WorkerDeleted
				wk.ProviderHandle = ""
			} else if wk.State == core.WorkerCompleted && successfulJobResult(wk.JobResult) {
				// GitHub completion can race provider inspection before terminal cleanup is queued.
				// Preserve the successful job result and queue idempotent deletion so providers
				// can still remove dependent resources (for example a DinD sidecar).
				wk.State = core.WorkerCleanupPending
				terminal = true
			} else {
				wk.State = core.WorkerFailed
				wk.FailureReason = "provider worker disappeared before cleanup"
				wk.FailedAt = now
				terminal = true
			}
		}
	}
	if res.Handle == nil && res.State == nil && wk.State == core.WorkerCleanupPending {
		wk.State = core.WorkerDeleted
		wk.ProviderHandle = ""
	}
	if err := s.Store.UpsertWorker(ctx, wk); err != nil {
		return err
	}
	if err := s.recordProviderDeleteResult(ctx, hostID, res); err != nil {
		return err
	}
	if terminal {
		_ = s.maybeAutoTunePool(ctx, wk.PoolID)
		return s.enqueueTerminalCleanup(ctx, &wk)
	}
	return nil
}

// recordProviderDeleteResult captures an authenticated agent's response to a
// durable delete command. It does not claim independent Docker volume or
// filesystem reclamation; production acceptance still requires host inventories.
// recordProviderDeleteResult captures authenticated agent acknowledgements of
// provider Delete calls, not independent evidence of disk/volume reclamation.
// Durably claim the command before updating process-local counters: a callback
// replay after an ACK failure must not inflate metrics or duplicate audit rows.
func (s *Service) recordProviderDeleteResult(ctx context.Context, hostID string, res agent.CommandResult) error {
	if s.Store == nil || res.CommandID == "" {
		return nil
	}
	rec, err := s.Store.GetCommand(ctx, res.CommandID)
	if err != nil || rec.Command.Type != agent.CommandDelete || rec.HostID != hostID || rec.Command.WorkerID != res.WorkerID {
		return nil
	}
	recorder, ok := s.Store.(store.ProviderDeleteResultRecorder)
	if !ok {
		return errors.New("provider deletion audit requires an idempotent recorder")
	}
	result := "agent_reported_failure"
	if res.OK {
		result = "agent_reported_success"
	}
	durationNanos := res.ProviderDeleteDurationNanos
	if durationNanos <= 0 || durationNanos > int64(24*time.Hour) {
		durationNanos = 0
	}
	detail := "provider delete result; command_id=" + res.CommandID + "; duration unavailable from agent"
	if durationNanos > 0 {
		detail = fmt.Sprintf("provider delete result; command_id=%s; agent_duration_ns=%d", res.CommandID, durationNanos)
	}
	inserted, err := recorder.RecordProviderDeleteResult(ctx, res.CommandID, hostID, res.WorkerID, core.AuditEvent{
		Actor: "agent:" + hostID, Action: "provider_delete_result", ResourceType: "worker", ResourceID: res.WorkerID, Result: result, SafeDetail: detail,
	})
	if err != nil {
		return err
	}
	if inserted && s.Metrics != nil {
		s.Metrics.ProviderDeletion(res.OK, time.Duration(durationNanos))
	}
	return nil
}
func successfulJobResult(result string) bool {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "success", "succeeded":
		return true
	default:
		return false
	}
}
func normalizeCompletionTime(started, completed, observed time.Time) time.Time {
	if completed.IsZero() || started.IsZero() || !completed.Before(started) {
		return completed
	}
	// Job-start callbacks are observed locally while GitHub supplies FinishTime.
	// Small delivery/clock skew can otherwise create impossible negative runtimes.
	if observed.After(started) {
		return observed
	}
	return started
}
func workerUsageSampleValid(wk *core.Worker, cpu, memory, envelopeCPU, envelopeMemory int64) bool {
	if wk == nil {
		return false
	}
	factor := int64(1)
	if wk.Isolation == core.IsolationContainerDinD && !wk.AggregateResources {
		factor = 2
	}
	if limit := wk.Resources.CPUMillis; limit > 0 {
		tolerance := int64(250)
		if limit/10 > tolerance {
			tolerance = limit / 10
		}
		if cpu > limit*factor+tolerance*factor {
			return false
		}
		if envelopeCPU > 0 && envelopeCPU > limit+tolerance {
			return false
		}
	}
	if limit := wk.Resources.MemoryBytes; limit > 0 {
		tolerance := int64(512 << 20)
		if limit/10 > tolerance {
			tolerance = limit / 10
		}
		if memory > limit*factor+tolerance*factor {
			return false
		}
		if envelopeMemory > 0 && envelopeMemory > limit+tolerance {
			return false
		}
	}
	return true
}
func recordWorkerUsage(wk *core.Worker, usage *provider.WorkerUsage, at time.Time, countSample bool) {
	if wk == nil || usage == nil {
		return
	}
	cpu := usage.CPUMillis
	if cpu < 0 {
		cpu = 0
	}
	memory := usage.MemoryBytes
	if memory < 0 {
		memory = 0
	}
	envelopeCPU := usage.EnvelopeCPUMillis
	if envelopeCPU < 0 {
		envelopeCPU = 0
	}
	envelopeMemory := usage.EnvelopeMemoryBytes
	if envelopeMemory < 0 {
		envelopeMemory = 0
	}
	wk.Usage.UpdatedAt = at
	if usage.AggregateResources {
		wk.Usage.AggregateResources = true
	}
	if !workerUsageSampleValid(wk, cpu, memory, envelopeCPU, envelopeMemory) {
		return
	}
	wk.Usage.CurrentCPUMillis = cpu
	wk.Usage.CurrentMemoryBytes = memory
	if !countSample {
		return
	}
	n := wk.Usage.Samples
	wk.Usage.AverageCPUMillis = rollingAverage(wk.Usage.AverageCPUMillis, cpu, n)
	wk.Usage.AverageMemoryBytes = rollingAverage(wk.Usage.AverageMemoryBytes, memory, n)
	if cpu > wk.Usage.PeakCPUMillis {
		wk.Usage.PeakCPUMillis = cpu
	}
	peakMemory := memory
	if usage.AggregateResources && envelopeMemory > peakMemory {
		peakMemory = envelopeMemory
	}
	if peakMemory > wk.Usage.PeakMemoryBytes {
		wk.Usage.PeakMemoryBytes = peakMemory
	}
	if wk.Isolation != core.IsolationContainerDinD {
		if envelopeCPU <= 0 {
			envelopeCPU = cpu
		}
		if envelopeMemory <= 0 {
			envelopeMemory = memory
		}
	}
	if envelopeCPU > wk.Usage.PeakEnvelopeCPUMillis {
		wk.Usage.PeakEnvelopeCPUMillis = envelopeCPU
	}
	if envelopeMemory > wk.Usage.PeakEnvelopeMemoryBytes {
		wk.Usage.PeakEnvelopeMemoryBytes = envelopeMemory
	}
	if n < ^uint64(0) {
		wk.Usage.Samples = n + 1
	}
}
func (s *Service) maybeAutoTunePool(ctx context.Context, poolID string) error {
	s.poolMutationMu.Lock()
	defer s.poolMutationMu.Unlock()
	p, err := s.Store.GetPool(ctx, poolID)
	if err != nil {
		return err
	}
	if !p.AutoTuneResources {
		return nil
	}
	workers, err := s.Store.ListWorkers(ctx)
	if err != nil {
		return err
	}
	rec := telemetry.RecommendResources(p, workers)
	if !rec.Ready {
		return nil
	}
	if rec.Recommended.CPUMillis == p.Resources.CPUMillis && rec.Recommended.MemoryBytes == p.Resources.MemoryBytes {
		return nil
	}
	candidate := p
	candidate.Resources.CPUMillis = rec.Recommended.CPUMillis
	candidate.Resources.MemoryBytes = rec.Recommended.MemoryBytes
	hosts, err := s.Store.ListHosts(ctx)
	if err != nil {
		return err
	}
	fits := false
	for _, host := range hosts {
		if scheduler.HostRejectionReason(host, candidate, core.Resources{}) == "" {
			fits = true
			break
		}
	}
	if !fits {
		_ = s.Store.AppendAudit(ctx, core.AuditEvent{Actor: "auto-tuner", Action: "auto_tune_resources", ResourceType: "pool", ResourceID: p.ID, Result: "skipped", SafeDetail: "recommended CPU/RAM does not fit any compatible empty host"})
		return nil
	}
	if err := core.ValidateRunnerPool(candidate); err != nil {
		return err
	}
	if err := s.Policy.ValidatePool(candidate); err != nil {
		return err
	}
	oldCPU, oldMemory := p.Resources.CPUMillis, p.Resources.MemoryBytes
	if err := s.Store.UpsertPool(ctx, candidate); err != nil {
		return err
	}
	_ = s.Store.AppendAudit(ctx, core.AuditEvent{Actor: "auto-tuner", Action: "auto_tune_resources", ResourceType: "pool", ResourceID: p.ID, Result: "ok", SafeDetail: fmt.Sprintf("cpu=%dm->%dm memory=%d->%d jobs=%d confidence=%s", oldCPU, candidate.Resources.CPUMillis, oldMemory, candidate.Resources.MemoryBytes, rec.Jobs, rec.Confidence)})
	return nil
}
func (s *Service) enqueueTerminalCleanup(ctx context.Context, wk *core.Worker) error {
	if wk == nil || strings.TrimSpace(wk.ProviderHandle) == "" || s.Hub == nil {
		return nil
	}
	if wk.State != core.WorkerCleanupPending {
		if !core.CanTransitionWorker(wk.State, core.WorkerCleanupPending) {
			return nil
		}
		wk.State = core.WorkerCleanupPending
		wk.UpdatedAt = time.Now().UTC()
		if err := s.Store.UpsertWorker(ctx, *wk); err != nil {
			return err
		}
	}
	h := provider.WorkerHandle{ProviderID: wk.ProviderHandle, HostID: wk.HostID}
	if err := s.Hub.Enqueue(wk.HostID, agent.Command{Type: agent.CommandDelete, WorkerID: wk.ID, Provider: wk.ProviderID, Handle: &h}); err != nil {
		return err
	}
	_ = s.Store.AppendAudit(ctx, core.AuditEvent{Actor: "control", Action: "cleanup_worker", ResourceType: "worker", ResourceID: wk.ID, Result: "queued", SafeDetail: wk.FailureReason})
	return nil
}
