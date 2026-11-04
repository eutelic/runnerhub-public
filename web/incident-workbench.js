/* Evidence-backed, read-only investigation of recorded RunnerHub workers.
   This does not report GitHub queue depth or promise a queued job exists. */
(function(scope){
  'use strict';
  const list=value=>Array.isArray(value)?value:[];
  const clean=value=>String(value??'').trim();
  const time=value=>{const t=Date.parse(clean(value));return Number.isFinite(t)?t:0};
  const number=value=>Number.isFinite(Number(value))?Number(value):0;
  const fields=['createdAt','queuedAt','registrationStartedAt','registeredAt','startedAt','completedAt','failedAt','updatedAt'];
  function repoFor(worker,pool){
    return clean(worker.repository)||(worker.scope?.owner&&worker.scope?.repository
      ? worker.scope.owner+'/'+worker.scope.repository
      : pool?.scope?.owner&&pool?.scope?.repository
      ? pool.scope.owner+'/'+pool.scope.repository:'');
  }
  function step(label,at){
    if(!at||String(at).startsWith('0001-01-01')||!time(at))return null;
    return {label,at:String(at)};
  }
  function timeline(worker){
    const events=[
      step('Runner requested',worker.createdAt),
      step('GitHub job queued (reported)',worker.queuedAt),
      step('Registration started',worker.registrationStartedAt),
      step('Runner registered',worker.registeredAt),
      step('Job started',worker.startedAt),
      step('Job completed',worker.completedAt),
      step('Runner failed',worker.failedAt)
    ].filter(Boolean);
    return events.sort((a,b)=>time(a.at)-time(b.at));
  }
  function runURL(repository,id){
    if(!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository))return '';
    const num=Number(id);
    if(!Number.isSafeInteger(num)||num<=0)return '';
    const [owner,name]=repository.split('/');
    return 'https://github.com/'+encodeURIComponent(owner)+'/'+encodeURIComponent(name)+'/actions/runs/'+num;
  }
  function summarize(workers,pools,hosts,listeners,runtimes,query){
    const poolByID=new Map(list(pools).map(x=>[x.id,x]));
    const hostByID=new Map(list(hosts).map(x=>[x.id,x]));
    const listenerByPool=new Map(list(listeners).map(x=>[x.poolId,x]));
    const runtimeByPool=new Map(list(runtimes).map(x=>[x.poolId,x]));
    const search=clean(query).toLowerCase();
    return list(workers).map(worker=>{
      const pool=poolByID.get(worker.poolId);
      const host=hostByID.get(worker.hostId);
      const repository=repoFor(worker,pool);
      const listener=listenerByPool.get(worker.poolId);
      const runtime=runtimeByPool.get(worker.poolId);
      const state=clean(worker.state)||'unknown';
      const runId=worker.workflowRunId||0;
      const evidence=[];
      if(worker.failureReason)evidence.push('Worker failure: '+clean(worker.failureReason));
      if(worker.providerOOMKilled)evidence.push('Provider reported out-of-memory termination.');
      if(listener?.state&&listener.state!=='ready')evidence.push('Current scale-set listener: '+listener.state);
      if(runtime?.reasons?.length)evidence.push('Current host eligibility: '+runtime.reasons.join('; '));
      const online=host?String(host.state).toLowerCase()==='online':false;
      if(host&&!online)evidence.push('Assigned host is currently '+clean(host.state));
      const githubURL=runURL(repository,runId);
      const runnerLabels=[...list(worker.runnerLabels),...list(worker.labels),...list(pool?.labels),worker.runnerLabel].filter(Boolean);
      return {
        id:clean(worker.id),repository,runId,job:clean(worker.jobName)||clean(worker.jobId)||'unassigned',
        pool:clean(pool?.scaleSetName)||clean(pool?.name)||clean(worker.poolId),
        host:clean(host?.name)||clean(worker.hostId)||'not assigned',
        state,result:clean(worker.jobResult)||state,
        failure:clean(worker.failureReason),events:timeline(worker),
        evidence,githubURL,
        updatedAt:clean(worker.updatedAt)||clean(worker.createdAt),
        listenerState:clean(listener?.state)||'unknown',
        listenerUpdatedAt:clean(listener?.updatedAt),
        runtimeState:clean(runtime?.state)||'unknown',
        lastFailureReason:clean(runtime?.lastFailureReason),
        searchText:[repository,runId,githubURL,worker.id,worker.jobName,worker.jobId,
          worker.runnerName,worker.workflowRef,runnerLabels.join(' '),pool?.scaleSetName,
          worker.failureReason,worker.jobResult,host?.name,worker.state].join(' ').toLowerCase()
      };
    }).filter(row=>!search||row.searchText.includes(search))
      .sort((a,b)=>time(b.updatedAt)-time(a.updatedAt)).slice(0,30)
      .map(({searchText,...row})=>row);
  }
  const api={summarize,timeline,runURL};
  scope.RunnerHubIncidentWorkbench=api;
  if(typeof module!=='undefined'&&module.exports)module.exports=api;
})(typeof window!=='undefined'?window:globalThis);
