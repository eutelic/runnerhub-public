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
        const failure = String(status?.detail || '');
        if (/scale-set worker not found/i.test(failure)) {
          warnings.push('Listener is retrying a started-job event for an unknown worker (HTTP 404). Inspect worker cleanup and the scale-set callback logs; this does not indicate CPU exhaustion or GitHub API throttling.');
        } else if (/\b409\b|message session.*conflict/i.test(failure)) {
          warnings.push('Listener is retrying a GitHub message-session conflict. Check for duplicate listener owners or overlapping restarts.');
        } else if (/\b429\b|rate.limit|\b403\b/i.test(failure)) {
          warnings.push('Listener may be blocked by GitHub API throttling or authorization; inspect the reported status and retry time.');
        } else {
          warnings.push('Scale-set listener reports backoff; GitHub API throttling or an upstream error may be involved.');
        }
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


  // Inventory only: reports installed repositories and configured pools. It does
  // not infer which labels live GitHub jobs request or claim any job is queued.
  function auditRepositories(repositories, pools, statuses, runtime) {
    const statusByPool = new Map(list(statuses).map(s => [String(s.poolId), s]));
    const healthByPool = new Map(list(runtime).map(h => [String(h.poolId), h]));
    const allPools = list(pools).filter(p => p?.scope?.kind === 'repository');
    return list(repositories).filter(r => typeof r?.fullName === 'string' && r.fullName.includes('/')).map(repo => {
      const name = repo.fullName;
      const matched = allPools.filter(p =>
        (String(p.scope.owner) + '/' + String(p.scope.repository)).toLowerCase() === name.toLowerCase());
      const labels = [...new Set(matched.flatMap(p =>
        [p.scaleSetName, ...list(p.labels)].map(x => String(x || '').trim()).filter(Boolean)))].sort();
      const standard = matched.find(p => {
        const name = String(p.scaleSetName || '').toLowerCase();
        return name === '' || name === 'runnerhub';
      });
      let code, explanation;
      if (!repo.private) {
        code = 'public-unsupported';
        explanation = 'Public repositories are not eligible for trusted RunnerHub worker onboarding.';
      } else if (!standard) {
        code = 'no-standard-pool';
        explanation = matched.length
          ? 'No standard runnerhub pool. Other labels are configured, but the standard label is unavailable.'
          : 'No repository-scoped runner pool. Connect this private repository in the dashboard.';
      } else {
        const status = statusByPool.get(String(standard.id));
        const health = healthByPool.get(String(standard.id));
        const listener = String(status?.state || 'unknown').toLowerCase();
        if (listener === 'error' || listener === 'backing_off' || listener === 'disabled') {
          code = 'listener-blocked';
          explanation = 'Standard listener: ' + listener + '. Inspect listener detail and GitHub App permissions.';
        } else if (listener !== 'ready') {
          code = 'listener-not-ready';
          explanation = 'Standard listener: ' + listener + '. GitHub job assignment is not yet verified.';
        } else if (health && (!health.healthy || number(health.onlineHosts) === 0)) {
          code = 'host-ineligible';
          const reasons = list(health.reasons).map(String).filter(Boolean);
          explanation = reasons.length
            ? 'No confirmed eligible capacity: ' + reasons.join('; ')
            : 'Listener ready, but runtime health reports no confirmed eligible capacity.';
        } else if (!health) {
          code = 'runtime-unverified';
          explanation = 'Listener ready, but host eligibility data is unavailable.';
        } else {
          code = 'ready';
          explanation = 'Standard listener ready and runtime health reports no known blockers; no claim about GitHub job queue state.';
        }
      }
      return {name, private: Boolean(repo.private), code, explanation, labels, standardPoolId: standard?.id || null};
    }).sort((a, b) => {
      const rank = {'no-standard-pool': 0, 'listener-blocked': 1, 'listener-not-ready': 2,
        'host-ineligible': 3, 'runtime-unverified': 4, 'ready': 5, 'public-unsupported': 6};
      return rank[a.code] - rank[b.code] || a.name.localeCompare(b.name);
    });
  }


  function planOnboarding(selected, repositories, pools, hosts, options) {
    const requested = [...new Set(list(selected).map(x => String(x).toLowerCase()))];
    const repoByName = new Map(list(repositories).map(r => [String(r?.fullName || '').toLowerCase(), r]));
    const configured = list(pools);
    const online = list(hosts).filter(h => h?.state === 'online');
    const publisherAvailable = online.some(h => h?.tags?.['isolation.container-dind'] === 'true');
    const fastAvailable = online.some(h => h?.tags?.fast === 'true');
    const operations = [];
    const skipped = [];
    for(const key of requested) {
      const repo = repoByName.get(key);
      if (!repo || !repo.private || !Number.isSafeInteger(Number(repo.installationId)) ||
          Number(repo.installationId) <= 0) {
        skipped.push({repository: key, reason:'Not an eligible, installed private repository.'});
        continue;
      }
      const name = repo.fullName;
      const matched = configured.filter(p =>
        p?.scope?.kind === 'repository' &&
        (String(p.scope.owner)+'/'+String(p.scope.repository)).toLowerCase() === key);
      const enabled = pool => Boolean(pool) && number(pool.githubInstallationId) > 0;
      const find = label => matched.find(p => String(p.scaleSetName || '').toLowerCase() === label);
      const base = {installationId:Number(repo.installationId),repository:name};
      if(!enabled(find('runnerhub'))) {
        operations.push({kind:'standard', repository:name, endpoint:'/api/v1/github/connect',
          payload:{...base,scaleSetName:'runnerhub',isolation:'container-trusted',
            maxConcurrency:2,resources:{cpuMillis:2500,memoryBytes:4294967296,diskBytes:21474836480}}});
      }
      if(options?.publisher && !enabled(find('runnerhub-publisher'))) {
        if(!publisherAvailable) skipped.push({repository:name,reason:'No online Docker-capable publisher host.'});
        else operations.push({kind:'publisher',repository:name,endpoint:'/api/v1/github/publisher',
          payload:{...base,enabled:true,maxConcurrency:1,
            resources:{cpuMillis:2500,memoryBytes:8589934592,diskBytes:42949672960}}});
      }
      if(options?.fast && !enabled(find('runnerhub-fast'))) {
        if(!fastAvailable) skipped.push({repository:name,reason:'No online host tagged fast.'});
        else operations.push({kind:'fast',repository:name,endpoint:'/api/v1/github/profile',
          payload:{...base,enabled:true,name:'fast',tags:['fast'],isolation:'container-trusted',
            maxConcurrency:2,resources:{cpuMillis:2500,memoryBytes:4294967296,diskBytes:21474836480}}});
      }
    }
    return {operations,skipped};
  }

  // Classify only the observed job labels and current pool evidence. An
  // apparently ready pool does not prove GitHub has assigned this queued job.
  function diagnoseQueuedJob(job, repository, pools, statuses, runtime) {
    const labels = list(job?.labels).map(String).filter(Boolean);
    const wanted = labels.filter(x => x.toLowerCase().startsWith('runnerhub'));
    if(!labels.length) return {code:'labels-unavailable',detail:'GitHub did not expose requested job labels; cannot identify the matching runner pool.'};
    if(!wanted.length) return {code:'other-runner', detail:'Job does not request a RunnerHub label.'};
    if(wanted.length !== 1 || labels.length !== 1) {
      return {code:'unsupported-label-combination',detail:'RunnerHub scale sets require a single matching runs-on label. Requested: '+labels.join(', ')};
    }
    const matchingRepo = list(pools).filter(p => p?.scope?.kind === 'repository' &&
      (String(p.scope.owner)+'/'+String(p.scope.repository)).toLowerCase() === String(repository).toLowerCase() &&
      number(p.githubInstallationId) > 0);
    if(!matchingRepo.length)return {code:'not-connected',detail:'No enabled RunnerHub pool exists for this repository.'};
    const pool = matchingRepo.find(p => String(p.scaleSetName || '').toLowerCase() === wanted[0].toLowerCase());
    if(!pool) return {code:'wrong-label', detail:'Requested '+wanted[0]+' but configured labels are: '+
      matchingRepo.map(p => p.scaleSetName).filter(Boolean).join(', ')};
    const status = list(statuses).find(s => String(s.poolId) === String(pool.id));
    const health = list(runtime).find(h => String(h.poolId) === String(pool.id));
    const state = String(status?.state || '').toLowerCase();
    const listenerDetail = String(status?.detail || '');
    if(state === 'error' || state === 'backing_off') {
      const jit = /jit|runner registration|registration token|invalid credentials/i.test(listenerDetail);
      return {code:jit?'jit-failure':'listener-failure', detail:'Listener '+state+(listenerDetail?': '+listenerDetail:'; inspect scale-set logs')};
    }
    if(state !== 'ready')return {code:'listener-not-ready',detail:'Listener '+(state||'unknown')+'; registration not confirmed.'};
    // Runtime history is not host capacity. Only the pool-health endpoint
    // reports current eligible hosts after placement-domain reservations.
    if(!health || typeof health.healthy !== 'boolean' ||
        health.onlineHosts == null || !Number.isFinite(Number(health.onlineHosts))) {
      return {code:'capacity-unverified',
        detail:'Listener ready, but live pool eligibility data is unavailable. Runtime history cannot establish current host capacity.'};
    }
    if(number(pool.maxConcurrency)>0 && number(health.activeWorkers)>=number(pool.maxConcurrency)) {
      return {code:'capacity',detail:'Configured pool concurrency limit occupied ('+
        number(health.activeWorkers)+'/'+number(pool.maxConcurrency)+').'};
    }
    if(!health.healthy || number(health.onlineHosts)===0) {
      const reasons=list(health.reasons).map(String);
      const detail=[health.lastFailureReason,...reasons].filter(Boolean).join('; ');
      const failed=/worker failed|worker failure|startup failure/i.test(detail);
      const resources=/(?:cpu|ram|memory|disk|gpu).*(?:exhaust|insuff)|(?:exhaust|insuff).*(?:cpu|ram|memory|disk|gpu)/i.test(detail);
      return {code:failed?'worker-failure':resources?'host-capacity':'no-compatible-host',
        detail:detail || 'No currently eligible host has confirmed free capacity for this pool.'};
    }
    return {code:'unknown',detail:'Listener ready and '+number(health.onlineHosts)+
      ' compatible host(s) have free resources according to RunnerHub. GitHub job assignment is not verified; inspect runner-group/label routing and scale-set logs.'};
  }

  const api = {summarize, auditRepositories, planOnboarding, diagnoseQueuedJob};
  scope.RunnerHubQueueDiagnostics = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
