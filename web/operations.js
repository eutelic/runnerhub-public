/* Read-only view of persisted operation records. No automatic polling. */
(function(scope){
  'use strict';
  const clean=value=>String(value??'').trim();
  function age(value,now=Date.now()){
    const at=Date.parse(clean(value));
    if(!Number.isFinite(at)||at<=0)return 'unknown';
    const seconds=Math.max(0,Math.floor((now-at)/1000));
    if(seconds<60)return seconds+'s';
    if(seconds<3600)return Math.floor(seconds/60)+'m';
    if(seconds<86400)return Math.floor(seconds/3600)+'h';
    return Math.floor(seconds/86400)+'d';
  }
  function stepLabel(value){
    const labels={
      'request-accepted':'Request accepted',
      'preparing-enrollment':'Preparing enrollment',
      'verifying-host-key':'Verifying SSH host key',
      'configuring-remote-host':'Configuring Docker and agent',
      'waiting-for-agent':'Waiting for agent heartbeat',
      'cleaning-enrollment-secret':'Removing temporary enrollment secret',
      'agent-ready':'Agent confirmed online',
      'bootstrap-failed':'Remote setup failed',
      'bootstrap-timed-out':'Setup timed out',
      'enrollment-cleanup-failed':'Enrollment needs cleanup',
      'native-kvm-unverified':'Native KVM capability unverified',
      'server-restarted':'Control plane restarted',
      'outcome-unknown':'Outcome unknown'
    };
    const key=clean(value);
    return labels[key]||key.split('-').filter(Boolean).map(part=>part[0]?.toUpperCase()+part.slice(1)).join(' ')||'Unknown stage';
  }
  function summarizeOperation(operation,now=Date.now()){
    const events=Array.isArray(operation?.events)?operation.events:[];
    return {
      id:clean(operation?.id),
      target:clean(operation?.target)||'Unknown target',
      state:clean(operation?.state)||'unknown',
      step:stepLabel(operation?.step),
      detail:clean(operation?.detail)||'No additional detail was recorded.',
      startedAge:age(operation?.startedAt,now),
      lastProgressAge:age(operation?.lastProgressAt,now),
      updatedAge:age(operation?.updatedAt,now),
      events:events.map(event=>({
        state:clean(event?.state)||'unknown',
        step:stepLabel(event?.step),
        at:clean(event?.at)
      }))
    };
  }
  function renderOperations(container,operations,now=Date.now()){
    container.replaceChildren();
    if(!Array.isArray(operations)||!operations.length){
      const empty=document.createElement('div');
      empty.className='empty';
      empty.textContent='No tracked operations were found.';
      container.append(empty);
      return;
    }
    for(const operation of operations){
      const summary=summarizeOperation(operation,now);
      const card=document.createElement('details');
      card.className='machine-operation-row';
      const heading=document.createElement('summary');
      heading.textContent=summary.target+' · '+summary.state+' · '+summary.step;
      const meta=document.createElement('p');
      meta.className='muted small';
      meta.textContent='Started '+summary.startedAge+' ago · Last confirmed progress '+summary.lastProgressAge+' ago · Updated '+summary.updatedAge+' ago';
      const detail=document.createElement('p');
      detail.textContent=summary.detail;
      const timeline=document.createElement('ol');
      for(const event of summary.events){
        const item=document.createElement('li');
        const at=Date.parse(event.at);
        const when=Number.isFinite(at)&&at>0?new Date(at).toLocaleString():'time unknown';
        item.textContent=event.step+' · '+event.state+' · '+when;
        timeline.append(item);
      }
      card.append(heading,meta,detail);
      if(summary.events.length)card.append(timeline);
      container.append(card);
    }
  }
  async function loadOperations(){
    const status=document.getElementById('operations-status');
    const list=document.getElementById('operations-list');
    const button=document.getElementById('operations-refresh');
    button.disabled=true;
    status.textContent='Refreshing persisted operations…';
    try{
      const headers={};
      const token=scope.sessionStorage?.getItem('runnerhub-token')||'';
      if(token)headers.authorization='Bearer '+token;
      const response=await scope.fetch('/api/v1/operations?limit=100',{headers});
      const body=await response.text();
      let data;
      try{data=JSON.parse(body)}catch{data={detail:body}}
      if(!response.ok)throw new Error(data.detail||data.error||('HTTP '+response.status));
      if(!Array.isArray(data))throw new Error('Invalid operations response');
      renderOperations(list,data);
      status.textContent='Showing '+data.length+' persisted operations. Use Refresh operations for a new snapshot.';
    }catch(error){
      list.replaceChildren();
      status.textContent='Operation history unavailable: '+String(error?.message||error)+'. Check admin access and RunnerHub connectivity.';
    }finally{button.disabled=false}
  }
  const api={age,stepLabel,summarizeOperation,renderOperations,loadOperations};
  scope.RunnerHubOperations=api;
  if(typeof module!=='undefined'&&module.exports)module.exports=api;
  if(scope.document){
    scope.document.getElementById('operations-refresh')?.addEventListener('click',loadOperations);
    loadOperations();
  }
})(typeof window!=='undefined'?window:globalThis);
