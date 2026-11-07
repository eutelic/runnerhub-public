# Public-repository / untrusted PR execution gate

**Status: NOT SUPPORTED.** This document defines the security work required before any
public repository is connected to RunnerHub. It does not enable public runners.
Keep public-repository workflows on GitHub-hosted runners until this
entire gate is satisfied.

## Assignment is a security boundary

A GitHub runner-group restriction can constrain **which repository/workflow**
receives a runner, but it is **not proof that a particular job was initiated by a
trusted actor**. A workflow triggered by `pull_request` may execute code
provided by a fork. Label selection, a local allowlist, a trusted repository
name, a job's `if:` expression, or a supposed private Docker daemon alone
does not turn that code into trusted code.

The current security inspector deliberately rejects **all public repositories**,
including `microvm` / `vm-provider` pool configurations. The trust and
isolation gates below must be implemented, verified and exposed in the UI
before modifying that policy. An unknown or incomplete GitHub runner-group
visibility or identity must fail closed.

## Proposed two-lane model

| Lane | Allowed source | Isolation | GitHub assignment |
| --- | --- | --- | --- |
| Trusted | Existing vetted private-repository jobs | Current private-only modes, according to existing policy | Existing labels and private-only groups |
| Public/untrusted (future) | Any public code, including forks and PRs | Independently attested, disposable VM/microVM only | Distinct untrusted labels and groups; never the trusted DinD/host lanes |

Treat any public-repository workflow as untrusted unless a separate, verifiable
trust decision can be enforced **before GitHub assigns the runner**. Do not
attempt to fix a late trust determination by changing the worker's privileges
after code has started. In particular, do not add the trusted `runnerhub`
label to a workflow accepting arbitrary fork commits.

## Required implementation proof

1. **Admission:** An explicit public/untrusted execution tier, default-off
   configuration, separate runner labels and a positive host capability
   attestation; refuse unavailable/unknown capabilities. No privileged
   DinD, host-persistent process, TrueNAS app, host socket, broad host network,
   writable host mounts, or runtime secrets.
2. **Host isolation:** Real VM/microVM separation established on the deployment
   hosts; disposable disk and network namespace; pinned reviewed guest image;
   cgroup/CPU, memory, disk, process and time limits; destruction after each
   job including failure/cancellation. No guest-controlled host-side API.
3. **Network policy:** Outbound connections needed for GitHub job operation are
   allowlisted and audited. Guests cannot reach control-plane/host-agent APIs,
   NAS datasets, Docker sockets, other worker subnets, metadata/management
   endpoints or private control networks. Confirm DNS, IPv4 and IPv6 behavior
   and bypass resistance on the *actual host*.
4. **Credentials:** GitHub App PEM, installation tokens, RunnerHub agent/admin
   secrets, registry credentials and host settings never enter the guest. JIT
   credentials are short-lived and scoped to a single ephemeral worker.
   Never elevate untrusted workflows with `pull_request_target` checkout of
   fork-supplied code.
5. **Scheduler:** An untrusted job cannot be admitted to a trusted pool, including
   on reconnect/retry, name/label overlap, inherited org runner groups,
   stale GitHub visibility data, or host capability drift. Recheck assignment
   and host policy at registration/provisioning, not just UI enrollment.
6. **Operations:** Operator-visible denial reasons, auditable allow/deny
   decisions, bounded startup and cleanup, and a safe disable/rollback switch.
   The UI must not claim a successful public connection before a real job
   safely runs.

## Acceptance matrix on real RunnerHub hosts

- Public fork PR: runs on the isolated untrusted lane; verify no secrets,
  host Docker/TrueNAS access, private-network access or worker-to-worker
  access. Try malicious guest commands as negative tests.
- Same-repository PR: executes according to an explicitly proven trust policy,
  not merely the event name or the `runnerhub` label.
- Protected trusted push: completes on the intended lane without unintentionally
  sharing worker state with earlier jobs.
- Failure cases: VM boot error, interrupted job, runner registration failure,
  network-policy failure, delayed GitHub status, and host restart all deny or
  clean up correctly. Host resource and volume usage return to baseline.
- Validate the public repository's CI workflow end to
  end with artifacts and logs, then migrate its supported events away from
  `ubuntu-latest` only after the negative tests pass.

**Merge/rollout rule:** Tests of the existing fail-closed policy may merge into
trusted RunnerHub without enabling public execution. Do **not** close internal follow-up or
migrate the public workflow until the full live acceptance matrix passes.
