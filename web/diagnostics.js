const statusEl=document.getElementById('diag-status');
const bundleEl=document.getElementById('support-bundle');
const refreshButton=document.getElementById('refresh-bundle');
const copyButton=document.getElementById('copy-bundle');

function token(){return sessionStorage.getItem('runnerhub-token')||''}

async function request(path){
  const headers={};
  if(token())headers.authorization='Bearer '+token();
  const response=await fetch(path,{headers});
  const text=await response.text();
  let value;
  try{value=JSON.parse(text)}catch{value=text}
  if(!response.ok){
    const detail=typeof value==='object'?(value.detail||value.error):value;
    throw new Error(detail||('HTTP '+response.status));
  }
  return value;
}

async function collect(name,path){
  try{return {ok:true,value:await request(path)}}
  catch(error){return {ok:false,error:String(error?.message||error)}}
}

let incidentSnapshot=null;
function renderIncidents(){
  const container=document.getElementById('incident-results');
  if(!container)return;
  container.replaceChildren();
  if(!incidentSnapshot){
    container.textContent='Run investigation is waiting for API data.';
    return;
  }
  const search=document.getElementById('incident-search')?.value||'';
  const [workers,pools,hosts,scalesets,runtime]=incidentSnapshot;
  if([workers,pools,hosts,scalesets,runtime].some(item=>!item.ok)){
    container.textContent='Worker investigation is unavailable: check API authentication or connectivity, then refresh the support bundle.';
    return;
  }
  const report=window.RunnerHubIncidentWorkbench.summarize(
    workers.value,pools.value,hosts.value,scalesets.value,runtime.value,search);
  if(!report.length){
    const empty=document.createElement('div');
    empty.className='empty';
    empty.textContent='No matching RunnerHub worker record. GitHub jobs can remain queued before RunnerHub creates a worker; check the workflow run on GitHub and the pool readiness above.';
    container.append(empty);
    return;
  }
  const label=document.createElement('p');
  label.className='muted small';
  label.textContent='Showing up to 30 most recent matching worker records. Evidence is a snapshot, not GitHub queue depth or live job completion percentage.';
  container.append(label);
  const formatTime=value=>{
    const date=new Date(value);
    return Number.isNaN(date.getTime())?value:date.toLocaleString();
  };
  for(const item of report){
    const card=document.createElement('details');
    card.className='incident-record';
    const title=document.createElement('summary');
    title.textContent=[item.repository||'unknown repository',item.job,item.result].join(' · ');
    card.append(title);
    const meta=document.createElement('p');
    meta.className='muted small';
    meta.textContent='Worker: '+item.id+' · host: '+item.host+' · class: '+item.pool+
      ' · listener: '+item.listenerState+' · runtime: '+item.runtimeState;
    card.append(meta);
    if(item.githubURL){
      const link=document.createElement('a');
      link.href=item.githubURL;link.target='_blank';link.rel='noopener noreferrer';
      link.textContent='Open GitHub workflow run';
      card.append(link);
    } else {
      const unknown=document.createElement('p');
      unknown.className='muted small';unknown.textContent='No validated GitHub run URL is recorded for this worker.';
      card.append(unknown);
    }
    if(item.evidence.length||item.lastFailureReason){
      const section=document.createElement('div');
      section.className='incident-warning';
      const head=document.createElement('strong');head.textContent='Recorded evidence';section.append(head);
      const list=document.createElement('ul');
      for(const line of [...item.evidence,item.lastFailureReason&&'Current runtime last failure: '+item.lastFailureReason].filter(Boolean)){
        const li=document.createElement('li');li.textContent=line;list.append(li);
      }
      section.append(list);card.append(section);
    }
    const timeline=document.createElement('ol');
    timeline.className='incident-timeline';
    for(const step of item.events){
      const li=document.createElement('li');
      li.textContent=step.label+' — '+formatTime(step.at);
      timeline.append(li);
    }
    if(item.events.length)card.append(timeline);
    else{
      const note=document.createElement('p');note.className='muted small';
      note.textContent='No usable timestamps are recorded for this worker.';
      card.append(note);
    }
    container.append(card);
  }
}
document.getElementById('incident-search')?.addEventListener('input',renderIncidents);
document.getElementById('incident-clear')?.addEventListener('click',()=>{
  document.getElementById('incident-search').value='';
  renderIncidents();
});

function sortWorkers(workers){
  if(!Array.isArray(workers))return workers;
  return [...workers].sort((a,b)=>String(b.updatedAt||b.createdAt||'').localeCompare(String(a.updatedAt||a.createdAt||''))).slice(0,100);
}

function showReadiness(pools, statuses, poolHealth) {
  const container=document.getElementById('queue-readiness');
  container.replaceChildren();
  if(!pools.ok||!statuses.ok||!poolHealth.ok){
    const note=document.createElement('p');
    note.textContent='Runner readiness unavailable: check authentication and API connectivity.';
    container.append(note);
    return;
  }
  const report=window.RunnerHubQueueDiagnostics.summarize(pools.value,statuses.value,poolHealth.value);
  if(!report.length){
    const note=document.createElement('p');
    note.textContent='No runner pools are currently configured.';
    container.append(note);
    return;
  }
  for(const item of report){
    const section=document.createElement('article');
    section.className='card';
    section.style.marginBottom='12px';
    const title=document.createElement('h3');
    title.textContent=item.label;
    const detail=document.createElement('p');
    const hosts=item.onlineHosts===null?'unknown':String(item.onlineHosts);
    detail.textContent='Listener: '+item.listener+
      ' | Eligible hosts: '+hosts+
      ' | Active workers: '+item.active+
      ' | Busy workers: '+item.busy+
      ' | Pool limit: '+item.capacity;
    const reason=document.createElement('p');
    reason.textContent=item.explanation;
    if(item.severity==='attention')reason.className='error';
    if(item.severity==='ready')reason.className='success';
    section.append(title,detail,reason);
    if(item.detail){
      const provider=document.createElement('p');
      provider.className='muted';
      provider.textContent='Listener detail: '+item.detail;
      section.append(provider);
    }
    container.append(section);
  }
}



async function inspectQueuedJobs(repository, output, pools, statuses, poolHealth) {
  output.replaceChildren();
  const loading=document.createElement('p');
  loading.textContent='Inspecting GitHub job labels and live RunnerHub pool status…';
  output.append(loading);
  try {
    const parts=repository.split('/');
    if(parts.length!==2 || !parts.every(part=>/^[A-Za-z0-9_.-]+$/.test(part)))throw new Error('Invalid GitHub repository name.');
    const path='/api/v1/github/repositories/'+parts.map(encodeURIComponent).join('/')+'/queued-jobs';
    const report=await request(path);
    output.replaceChildren();
    const summary=document.createElement('p');
    summary.textContent='Checked '+report.checkedRuns+' recent queued/in-progress runs. '+
      (report.truncated?'Results are incomplete because GitHub returned more runs/jobs than the per-request cap. ':'')+
      'This is a point-in-time snapshot, not proof of job acceptance.';
    output.append(summary);
    const relevant=(Array.isArray(report.jobs)?report.jobs:[]).filter(job=>
      !Array.isArray(job.labels)||!job.labels.length||job.labels.some(label=>String(label).toLowerCase().startsWith('runnerhub')));
    if(!relevant.length){
      const note=document.createElement('p');
      note.textContent='No pending jobs requesting a RunnerHub label or missing label data were found in the inspected runs.';
      output.append(note);
      return;
    }
    for(const job of relevant){
      const diagnosis=window.RunnerHubQueueDiagnostics.diagnoseQueuedJob(job,repository,pools,statuses,poolHealth);
      const card=document.createElement('div');
      card.className='resource-note';
      const name=document.createElement('strong');
      name.textContent=job.name+' · '+job.status;
      const labels=document.createElement('p');
      labels.textContent='Requested: '+(job.labels?.length?job.labels.join(', '):'labels unavailable')+' · Blocker: '+diagnosis.code;
      const reason=document.createElement('p');
      reason.textContent=diagnosis.detail;
      card.append(name,labels,reason);
      const url=job.url||job.runUrl;
      try{
        const u=new URL(url);
        if(u.protocol==='https:'&&u.hostname==='github.com'&&u.pathname.startsWith('/')){
          const link=document.createElement('a');
          link.href=u.href;
          link.target='_blank';
          link.rel='noopener noreferrer';
          link.textContent='Open GitHub job';
          card.append(link);
        }
      }catch{}
      output.append(card);
    }
  }catch(error){
    output.replaceChildren();
    const note=document.createElement('p');
    note.className='error';
    note.textContent='GitHub job inspection unavailable: '+String(error?.message||error)+
      '. Verify GitHub Actions read permission and authentication; no queue diagnosis was inferred.';
    output.append(note);
  }
}

function showRepositoryAudit(repositories, pools, statuses, poolHealth) {
  const container=document.getElementById('repository-audit');
  if(!container)return;
  container.replaceChildren();
  const note=document.createElement('p');
  if(!repositories.ok){
    note.textContent='Repository inventory unavailable: sign in with GitHub or check App installation access. Pool readiness above may still be available.';
    container.append(note);
    return;
  }
  if(!pools.ok||!statuses.ok||!poolHealth.ok){
    note.textContent='Repository audit unavailable: pool eligibility or listener status could not be retrieved.';
    container.append(note);
    return;
  }
  const report=window.RunnerHubQueueDiagnostics.auditRepositories(
    repositories.value,pools.value,statuses.value,poolHealth.value);
  const privateRepos=report.filter(r=>r.private);
  const disconnected=privateRepos.filter(r=>r.code==='no-standard-pool');
  const healthy=privateRepos.filter(r=>r.code==='ready');
  note.textContent=privateRepos.length+' eligible private repositories visible · '+
    disconnected.length+' missing standard pools · '+
    healthy.length+' reporting ready. Optional publisher/fast labels are listed only when configured.';
  container.append(note);
  if(!report.length){
    const empty=document.createElement('p');
    empty.textContent='No repositories exposed to the signed-in identity by the RunnerHub GitHub App.';
    container.append(empty);
  }
  for(const entry of report){
    const article=document.createElement('article');
    article.className='card';
    article.style.marginBottom='12px';
    const heading=document.createElement('h3');
    // Repository names come from GitHub, not a user-authored URL.
    // Validate owner/repo components anyway before making an external link.
    const parts=entry.name.split('/');
    if(parts.length===2&&parts.every(part=>/^[A-Za-z0-9_.-]+$/.test(part))){
      const link=document.createElement('a');
      link.href='https://github.com/'+parts.map(encodeURIComponent).join('/')+'/actions';
      link.target='_blank';
      link.rel='noopener noreferrer';
      link.textContent=entry.name+' · Actions';
      heading.append(link);
    }else{
      heading.textContent=entry.name;
    }
    const status=document.createElement('p');
    status.textContent='Audit status: '+entry.code+' · configured labels: '+
      (entry.labels.length?entry.labels.join(', '):'none');
    const detail=document.createElement('p');
    detail.textContent=entry.explanation;
    if(entry.code!=='ready')detail.className='muted';
    article.append(heading,status,detail);
    if(entry.private){
      const button=document.createElement('button');
      button.className='secondary compact';
      button.type='button';
      button.textContent='Inspect pending GitHub jobs';
      const result=document.createElement('div');
      button.addEventListener('click',()=>{
        button.disabled=true;
        inspectQueuedJobs(entry.name,result,pools.value,statuses.value,poolHealth.value)
          .finally(()=>{button.disabled=false});
      });
      article.append(button,result);
    }
    container.append(article);
  }
}

async function buildBundle(){
  refreshButton.disabled=true;
  copyButton.disabled=true;
  statusEl.className='';
  statusEl.textContent='Collecting diagnostics…';
  try{
    const [auth,hosts,pools,workers,audit,scalesets,runtime,poolHealth,versions,metrics,repositories]=await Promise.all([
      collect('auth','/auth/github/status'),
      collect('hosts','/api/v1/hosts'),
      collect('pools','/api/v1/pools'),
      collect('workers','/api/v1/workers'),
      collect('audit','/api/v1/audit?limit=200'),
      collect('scalesets','/api/v1/scalesets/status'),
      collect('runtime','/api/v1/runtime-health'),
      collect('poolHealth','/api/v1/pools/health'),
      collect('versions','/api/v1/fleet/versions'),
      collect('metrics','/api/v1/metrics'),
      collect('repositories','/api/v1/github/connected-repositories')
    ]);
    const fullWorkerEvidence=workers.ok?{ok:true,value:workers.value}:workers;
    if(workers.ok)workers.value=sortWorkers(workers.value);
    showReadiness(pools,scalesets,poolHealth);
    showRepositoryAudit(repositories,pools,scalesets,poolHealth);
    incidentSnapshot=[fullWorkerEvidence,pools,hosts,scalesets,runtime];
    renderIncidents();
    if(auth.ok&&auth.value&&typeof auth.value==='object'){
      delete auth.value.csrf;
    }
    const bundle={
      runnerhubSupportBundleVersion:1,
      generatedAt:new Date().toISOString(),
      page:location.origin,
      notes:[
        'Bundle is assembled from authenticated RunnerHub APIs.',
        'Worker startup diagnostics are redacted server-side/agent-side before storage.',
        'RUNNERHUB_DIAGNOSTICS_ENABLED=true enables capture for clean pre-job Docker exits.'
      ],
      auth,
      hosts,
      pools,
      workers,
      audit,
      scaleSets:scalesets,
      runtimeHealth:runtime,
      poolEligibility:poolHealth,
      versions,
      metrics
    };
    bundleEl.value=JSON.stringify(bundle,null,2);
    statusEl.className='success';
    statusEl.textContent='Support bundle ready. Review it, then copy it into an issue or diagnostic session.';
  }catch(error){
    statusEl.className='error';
    statusEl.textContent=String(error?.message||error);
  }finally{
    refreshButton.disabled=false;
    copyButton.disabled=false;
  }
}

async function copyBundle(){
  if(!bundleEl.value)await buildBundle();
  try{
    await navigator.clipboard.writeText(bundleEl.value);
    statusEl.className='success';
    statusEl.textContent='Support bundle copied to clipboard.';
  }catch{
    bundleEl.focus();
    bundleEl.select();
    const ok=document.execCommand('copy');
    statusEl.className=ok?'success':'error';
    statusEl.textContent=ok?'Support bundle copied to clipboard.':'Clipboard access failed; copy the selected text manually.';
  }
}

refreshButton.addEventListener('click',buildBundle);
copyButton.addEventListener('click',copyBundle);
buildBundle();
