# ADR: GitHub remains the authoritative CI job queue

**Status:** Accepted for the current RunnerHub architecture (2026-11-07)  
**Related:** internal follow-up, internal follow-up (latency instrumentation), internal follow-up (cross-pool fairness), internal follow-up (cleanup)

## Decision

Use GitHub Actions as the source of truth for queued, assigned, canceled,
and requeued jobs. Do **not** advertise unlimited scale-set capacity to
pre-acquire every queued job, and do **not** introduce a second durable
RunnerHub-owned queue of GitHub jobs. RunnerHub owns **admission of physical
runner resources**, not GitHub's job assignment order.

An optional future, bounded *admission-intent* structure may coordinate
already-assigned demand between pools, but it must never acknowledge or hide
GitHub jobs independently of the supported scale-set message protocol.

| Architecture | Potential benefit | Failure modes / decision |
| --- | --- | --- |
| GitHub authoritative queue + honest admission | Jobs remain visible in GitHub; supported ACK, cancellation and retry; workers provision only when host budget permits | **Adopt.** Requires conservative capacity advertisements, fast wakeup, idempotent admission, and measured provisioning time |
| Bounded RunnerHub admission-intent queue | Fairness across eligible pools and avoiding unnecessary repeated JIT attempts | Consider later under internal follow-up only with durable leases, GitHub assignment correlation and cancellation reconciliation; not a replacement for the GitHub queue |
| Optimistic/unlimited advertised capacity + private backlog | Could seemingly move jobs out of GitHub's queued state earlier | **Reject.** Inflates assignments beyond physical slots, risks pickup-timeout requeues and failed acquisitions, hides actual bottlenecks and does not create compute capacity |

## Protocol and ownership constraints

- The actions/scaleset listener reports demand through
  `Statistics.TotalAssignedJobs` (including waiting and running work).
  Individual JobAvailable message batches can be truncated and cannot be
  summed into an authoritative global backlog.
- The GitHub scale-set service owns assignment, job cancellation, message
  acknowledgment/redelivery and the definitive workflow job identity.
  RunnerHub cannot freely reorder individual GitHub jobs.
- The SDK acknowledges messages only after successful listener processing.
  Replayed job messages and retried `scale-workers` HTTP requests must be
  safe after control-plane restart or a dropped HTTP response.
- RunnerHub's scheduler already serializes host selection and reservation
  under the server admission lock, including placement-domain aliasing.
  Pools must also respect `MaxConcurrency`, host drain/offline state, resource
  envelopes, DinD conservative x2 CPU/RAM accounting when aggregate cgroup
  containment is unverified, and cleanup reservations.

## Current implementation and this increment

At registration, `MaxRunners` is bounded by the configured pool
`MaxConcurrency`. That bound is **not a proof of physically available
capacity**: hosts may be offline, drained, constrained by selectors, or busy
with other pools. On every desired-runner message, RunnerHub queries live pool
health *before* generating a JIT runner. The server makes an independent
synchronized admission decision. If capacity changes between JIT generation
and admission, it returns conflict, and the listener removes the unused GitHub
runner registration.

This increment:

1. Adds an indexed durable lookup on `(poolId,runnerName)`, **including
   deleted records**, so redelivered create requests return the previously
   admitted worker rather than reserving another physical slot.
2. Requires the original nonzero GitHub runner ID, pool, name, repository and
   compatible workflow identity before a duplicate can be treated as a replay.
   A reused name with a different runner ID fails closed.
3. Holds the control-plane admission lock through persisting the create
   command, so the acknowledged worker is not missing its host command.
4. Tests replay after a simulated controller restart and after deletion.

These changes are **not** a multi-controller distributed admission lease:
the current service has a single active control plane and one in-process
scheduler lock. A future HA controller must use a durable, transactional
admission claim/uniqueness constraint, and migration must first reconcile
historical duplicate names.

## Capacity work remaining

Do not clamp a scale set permanently to a snapshot of `free slots` at
startup: a temporarily saturated or offline host could yield zero, and fixed
listener configuration would stay undersized when host capacity returns.
Conversely, a static pool maximum alone is not current physical capacity.

Next, inspect whether the supported GitHub scale-set API and SDK allow safe
runtime max-capacity updates without replacing active sessions. If supported,
report a conservative **eligible physical capacity ceiling** including active
reserved workers, pool limit, compatible placement domains and shared-host
resources, with hysteresis and a safe fallback when measurements are stale.
Document GitHub routing semantics before turning this on.

## Acceptance evidence still required

- 1/20/50/100-job controlled bursts, two listeners contending for one host,
  and retries under server response loss; no duplicate resource reservation.
- GitHub cancellation/requeue while a provider is slow or disappears, late
  started/completed message redelivery, and manager restarts.
- No idle compatible host while eligible work waits without an explained
  bottleneck; compare queue-to-start p50/p95, failed/canceled fraction and
  admission latency to internal follow-up's baseline.
- Keep source-level merge separate from deployed controller and production
  acceptance. No host health or throughput claims come from this ADR.

## Sources

- https://github.com/actions/scaleset#autoscaling
- https://github.com/actions/scaleset#how-the-message-api-works
- https://docs.github.com/en/actions/reference/runners/self-hosted-runners
