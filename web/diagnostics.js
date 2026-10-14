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

function sortWorkers(workers){
  if(!Array.isArray(workers))return workers;
  return [...workers].sort((a,b)=>String(b.updatedAt||b.createdAt||'').localeCompare(String(a.updatedAt||a.createdAt||''))).slice(0,100);
}

function showReadiness(pools, statuses, runtime) {
  const container=document.getElementById('queue-readiness');
  container.replaceChildren();
  if(!pools.ok||!statuses.ok||!runtime.ok){
    const note=document.createElement('p');
    note.textContent='Runner readiness unavailable: check authentication and API connectivity.';
    container.append(note);
    return;
  }
  const report=window.RunnerHubQueueDiagnostics.summarize(pools.value,statuses.value,runtime.value);
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

async function buildBundle(){
  refreshButton.disabled=true;
  copyButton.disabled=true;
  statusEl.className='';
  statusEl.textContent='Collecting diagnostics…';
  try{
    const [auth,hosts,pools,workers,audit,scalesets,runtime,versions,metrics]=await Promise.all([
      collect('auth','/auth/github/status'),
      collect('hosts','/api/v1/hosts'),
      collect('pools','/api/v1/pools'),
      collect('workers','/api/v1/workers'),
      collect('audit','/api/v1/audit?limit=200'),
      collect('scalesets','/api/v1/scalesets/status'),
      collect('runtime','/api/v1/runtime-health'),
      collect('versions','/api/v1/fleet/versions'),
      collect('metrics','/api/v1/metrics')
    ]);
    if(workers.ok)workers.value=sortWorkers(workers.value);
    showReadiness(pools,scalesets,runtime);
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
