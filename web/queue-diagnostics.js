/* Read-only diagnosis from existing RunnerHub API state, not GitHub queue claims. */
(function (scope) {
  'use strict';

  function list(value) { return Array.isArray(value) ? value : []; }
  function number(value) { return Number.isFinite(Number(value)) ? Number(value) : 0; }
  function label(pool) {
    const source = pool?.scope || {};
    const repo = source.owner && source.repository
      ? source.owner + '/' + source.repository : (source.owner || '');
    return [repo, pool?.scaleSetName || pool?.name || pool?.id].filter(Boolean).join(' — ');
  }

  function summarize(pools, statuses, runtime) {
    const byStatus = new Map(list(statuses).map(row => [String(row.poolId), row]));
    const byHealth = new Map(list(runtime).map(row => [String(row.poolId), row]));
    return list(pools).filter(pool => pool?.id).map(pool => {
      const status = byStatus.get(String(pool.id));
      const health = byHealth.get(String(pool.id));
      const state = String(status?.state || 'unknown').toLowerCase();
      const warnings = [];
      const installation = number(pool.githubInstallationId ?? pool.githubInstallationID ?? pool.GitHubInstallationID);
      const capacity = number(pool.maxConcurrency);
      const active = number(health?.activeWorkers);
      const busy = number(health?.busyWorkers);
      if (!installation) {
        warnings.push('No GitHub App installation attached to this pool.');
      }
      if (state === 'error') {
        warnings.push('Scale-set listener reports an error.');
      } else if (state === 'backing_off') {
        warnings.push('Scale-set listener reports backoff; GitHub API throttling or an upstream error may be involved.');
      } else if (state === 'starting' || state === 'pending') {
        warnings.push('Scale-set listener is initializing; jobs may have to wait.');
      } else if (state === 'disabled') {
        warnings.push('Scale-set listener is disabled.');
      } else if (state === 'unknown') {
        warnings.push('No listener status has been reported to RunnerHub.');
      }
      if (health && !health.healthy) {
        const reasons = list(health.reasons);
        warnings.push(reasons.length
          ? 'Host eligibility: ' + reasons.join('; ')
          : 'Runtime health reports a degraded pool.');
      }
      if (capacity > 0 && active >= capacity && busy > 0) {
        warnings.push('Configured concurrency is occupied; additional jobs can wait for capacity.');
      }
      const severity = warnings.length ? 'attention' : (state === 'ready' ? 'ready' : 'unknown');
      return {
        poolId: String(pool.id),
        label: label(pool) || String(pool.id),
        listener: state,
        detail: String(status?.detail || ''),
        onlineHosts: health ? number(health.onlineHosts) : null,
        active, busy, capacity,
        updatedAt: String(status?.updatedAt || ''),
        severity,
        explanation: warnings.length ? warnings.join(' ') :
          (state === 'ready' && health
            ? 'Listener reports ready and runtime health reports no known blockers.'
            : 'Not enough status data to infer readiness.'),
      };
    });
  }

  const api = {summarize};
  scope.RunnerHubQueueDiagnostics = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
