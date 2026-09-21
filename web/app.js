const els={
  token:document.getElementById('apitoken'),
  hosts:document.getElementById('hosts'),
  pools:document.getElementById('pools'),
  activeRunners:document.getElementById('active-runners'),
  activeRunnerSummary:document.getElementById('active-runner-summary'),
  workers:document.getElementById('workers'),
  workerSummary:document.getElementById('worker-summary'),
  audit:document.getElementById('audit'),
  msg:document.getElementById('msg'),
  repos:document.getElementById('repos'),
  authstatus:document.getElementById('authstatus'),
  account:document.getElementById('account'),
  login:document.getElementById('login'),
  install:document.getElementById('install'),
  logout:document.getElementById('logout'),
  legacy:document.getElementById('legacy-auth')
};
let csrf='';
let auth={enabled:false,authenticated:false};

els.token.value=sessionStorage.getItem('runnerhub-token')||'';
els.token.addEventListener('input',()=>{sessionStorage.setItem('runnerhub-token',els.token.value);refresh()});
document.getElementById('create').addEventListener('click',createPool);
document.getElementById('machine-scan-fingerprint').addEventListener('click',scanMachineHostKey);
document.getElementById('add-machine').addEventListener('click',addMachine);
els.logout.addEventListener('click',logout);
els.hosts.addEventListener('click',e=>{const button=e.target.closest('button[data-host-tags]');if(button)saveHostTags(button)});
els.repos.addEventListener('click',e=>{
  const profile=e.target.closest('button[data-profile-action]');
  if(profile){saveRunnerProfile(profile);return}
  const publisher=e.target.closest('button[data-publisher-action]');
  if(publisher){savePublisher(publisher);return}
  const button=e.target.closest('button[data-resource-action]');
  if(button)saveRepositoryResources(button);
});

function esc(value){return String(value??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]))}
function show(el,on){el.classList.toggle('hidden',!on)}
function message(text,kind=''){els.msg.className=kind;els.msg.textContent=text||''}
function gib(bytes){return bytes?Number((bytes/1073741824).toFixed(2)):0}
function cpuCores(millis){return millis?Number((millis/1000).toFixed(2)):0}
function usageCPU(millis){return Number.isFinite(Number(millis))&&Number(millis)>=0?cpuCores(Number(millis))+' cores':'—'}
function usageRAM(bytes){return Number.isFinite(Number(bytes))&&Number(bytes)>=0?gib(Number(bytes))+' GiB':'—'}
function sizingText(rec){const s=rec?.resourceSizing;if(!s?.ready)return s?.jobs?('learning ('+s.jobs+' jobs)'):'learning';return cpuCores(s.recommended?.cpuMillis)+' cores / '+gib(s.recommended?.memoryBytes)+' GiB'}
function observedText(rec){const s=rec?.resourceSizing;if(!s?.ready)return s?.jobs?('learning ('+s.jobs+' jobs)'):'—';return cpuCores(s.cpuPeakP95Millis)+' cores / '+gib(s.memoryPeakP95Bytes)+' GiB'}
function fmtDuration(value){return value&&value!=='0s'?value:'—'}
function bottleneckLabel(value){return ({pool_concurrency:'pool limit',host_resources:'host resources',no_compatible_host:'no compatible host'})[value]||value||'—'}
function reservedCPU(capacity){const used=cpuCores(capacity?.reserved?.cpuMillis);const total=cpuCores(capacity?.capacity?.cpuMillis);return total?used+' / '+total+' cores':'—'}
function reservedRAM(capacity){const used=gib(capacity?.reserved?.memoryBytes);const total=gib(capacity?.capacity?.memoryBytes);return total?used+' / '+total+' GiB':'—'}
function repoName(pool){return pool?.scope?.repository?pool.scope.owner+'/'+pool.scope.repository:pool?.scope?.owner||''}
function isPublisherPool(pool){return Boolean(pool)&&(String(pool.scaleSetName||'').toLowerCase()==='runnerhub-publisher'||(pool.labels||[]).some(x=>String(x).toLowerCase()==='runnerhub-publisher'))}
function isStandardPool(pool){const name=String(pool?.scaleSetName||'').toLowerCase();return Boolean(pool)&&(name===''||name==='runnerhub')}
function runnerClass(pool){if(!pool)return 'standard';if(isPublisherPool(pool))return 'publisher';if(isStandardPool(pool))return 'standard';return pool.scaleSetName||'custom'}
function isPublisherEnabled(pool){return Boolean(pool)&&Number(pool.githubInstallationId)>0}
function isZeroTime(value){return !value||String(value).startsWith('0001-01-01')}
function fmtTime(value){
  if(isZeroTime(value))return '—';
  const d=new Date(value);
  if(Number.isNaN(d.getTime()))return String(value);
  return d.toLocaleString([],{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});
}
function workerTimestamp(worker){return worker.updatedAt||worker.failedAt||worker.completedAt||worker.startedAt||worker.registeredAt||worker.createdAt||''}
function fmtAge(value){
  if(isZeroTime(value))return '—';
  const started=new Date(value).getTime();
  if(!Number.isFinite(started))return '—';
  const seconds=Math.max(0,Math.floor((Date.now()-started)/1000));
  if(seconds<60)return seconds+'s';
  const minutes=Math.floor(seconds/60);
  if(minutes<60)return minutes+'m';
  const hours=Math.floor(minutes/60);
  if(hours<24)return hours+'h '+(minutes%60)+'m';
  const days=Math.floor(hours/24);
  return days+'d '+(hours%24)+'h';
}
function workerDisplayState(worker){
  if(worker.jobResult){
    const r=String(worker.jobResult).toLowerCase();
    if(r==='succeeded'||r==='success')return 'succeeded';
    if(r==='failed'||r==='failure')return 'failed';
    return r;
  }
  return worker.state||'unknown';
}

async function j(path,opts={}){
  opts.headers=opts.headers||{};
  if(els.token.value)opts.headers.authorization='Bearer '+els.token.value;
  const method=(opts.method||'GET').toUpperCase();
  if(csrf&&!['GET','HEAD','OPTIONS'].includes(method))opts.headers['X-RunnerHub-CSRF']=csrf;
  const r=await fetch(path,opts);
  const t=await r.text();
  let v;try{v=JSON.parse(t)}catch{v=t}
  if(!r.ok)throw new Error(typeof v==='object'?(v.detail||v.error):v);
  return v;
}

function tab(rows,cols){
  if(!Array.isArray(rows)||!rows.length)return '<span class="muted">None</span>';
  return '<div class="table-wrap"><table><tr>'+cols.map(c=>'<th>'+esc(c[0])+'</th>').join('')+'</tr>'+
    rows.map(r=>'<tr>'+cols.map(c=>'<td>'+esc(c[1](r))+'</td>').join('')+'</tr>').join('')+'</table></div>';
}

function hostTagText(host){return Object.keys(host?.userTags||{}).sort().join(', ')}
function renderHosts(hosts){
  if(!Array.isArray(hosts)||!hosts.length){els.hosts.innerHTML='<span class="muted">None</span>';return}
  els.hosts.innerHTML='<div class="table-wrap"><table><tr><th>Name</th><th>State</th><th>CPU</th><th>Memory</th><th>Priority</th><th>Tags</th><th></th></tr>'+
    hosts.map(h=>'<tr>'+
      '<td>'+esc(h.name)+'</td>'+
      '<td>'+esc(h.state)+'</td>'+
      '<td>'+esc(h.capacity?.cpuMillis?cpuCores(h.capacity.cpuMillis)+' cores':'unknown')+'</td>'+
      '<td>'+esc(h.capacity?.memoryBytes?gib(h.capacity.memoryBytes)+' GiB':'unknown')+'</td>'+
      '<td><input class="inline-tags" data-host-priority-input="'+esc(h.id)+'" type="number" min="-1000" max="1000" step="1" value="'+esc(Number(h.priority||0))+'"></td>'+
      '<td><input class="inline-tags" data-host-tag-input="'+esc(h.id)+'" value="'+esc(hostTagText(h))+'" placeholder="gpu, high-memory"></td>'+
      '<td><button class="secondary compact" data-host-tags="'+esc(h.id)+'">Save</button></td>'+
    '</tr>').join('')+'</table></div>';
}

function renderActiveRunners(workers,poolByID,hostByID,progressByWorker){
  const all=Array.isArray(workers)?workers:[];
  const terminal=new Set(['deleted','completed','failed']);
  const active=all.filter(w=>!terminal.has(String(w.state||'').toLowerCase())).sort((a,b)=>{
    const rank={busy:0,ready:1,registering:2,starting:3,provisioning:4,requested:5,deleting:6,cleanup_pending:7};
    const ar=rank[String(a.state||'').toLowerCase()]??8;
    const br=rank[String(b.state||'').toLowerCase()]??8;
    if(ar!==br)return ar-br;
    return new Date(a.createdAt||0)-new Date(b.createdAt||0);
  });
  const cpu=active.reduce((sum,w)=>sum+Number(w.resources?.cpuMillis||0),0);
  const memory=active.reduce((sum,w)=>sum+Number(w.resources?.memoryBytes||0),0);
  els.activeRunnerSummary.textContent=active.length+' active · '+cpuCores(cpu)+' cores · '+gib(memory)+' GiB allocated';
  if(!active.length){
    els.activeRunners.innerHTML='<div class="empty">No active runners.</div>';
    return;
  }

  els.activeRunners.innerHTML=tab(active,[
    ['Repository',x=>x.repository||repoName(poolByID?.get(x.poolId))||'—'],
    ['Type',x=>runnerClass(poolByID?.get(x.poolId))],
    ['Job',x=>x.jobName||(String(x.state||'').toLowerCase()==='ready'?'idle / waiting':'startup')],
    ['Status',x=>x.state||'unknown'],
    ['Step progress',x=>{const p=progressByWorker?.get(x.id);return p?.available&&p.totalSteps>0?p.completedSteps+' / '+p.totalSteps+' · '+p.percent+'%':'—'}],
    ['Current step',x=>progressByWorker?.get(x.id)?.currentStep||'—'],
    ['Host',x=>hostByID?.get(x.hostId)?.name||x.hostId||'pending'],
    ['CPU',x=>x.resources?.cpuMillis?cpuCores(x.resources.cpuMillis)+' cores':'—'],
    ['RAM',x=>x.resources?.memoryBytes?gib(x.resources.memoryBytes)+' GiB':'—'],
    ['Disk',x=>x.resources?.diskBytes?gib(x.resources.diskBytes)+' GiB':'—'],
    ['Runner started',x=>fmtTime(x.createdAt)],
    ['Job started',x=>fmtTime(x.startedAt)],
    ['Age',x=>fmtAge(x.createdAt)]
  ]);
}

function renderWorkers(workers,poolByID){
  const all=Array.isArray(workers)?workers:[];
  const terminal=new Set(['deleted','completed','failed']);
  const active=all.filter(w=>!terminal.has(String(w.state||'').toLowerCase()));
  const jobs=all.filter(w=>terminal.has(String(w.state||'').toLowerCase())&&(w.jobName||w.jobResult||w.workflowRunId));
  const failureCutoff=Date.now()-15*60*1000;
  const freshStartupFailures=all.filter(w=>{
    if(!w.failureReason||w.jobName||w.jobResult||w.workflowRunId)return false;
    const t=new Date(workerTimestamp(w)).getTime();
    return Number.isFinite(t)&&t>=failureCutoff;
  }).slice(0,2);

  const wanted=new Set();
  active.slice(0,10).forEach(w=>wanted.add(w.id));
  jobs.slice(0,8).forEach(w=>wanted.add(w.id));
  freshStartupFailures.forEach(w=>wanted.add(w.id));
  const visible=all.filter(w=>wanted.has(w.id)).slice(0,10);

  els.workerSummary.textContent=active.length+' active · '+Math.min(jobs.length,8)+' recent job'+(Math.min(jobs.length,8)===1?'':'s');
  if(!visible.length){
    els.workers.innerHTML='<div class="empty">No active or recent runner activity.</div>';
    return;
  }

  els.workers.innerHTML=tab(visible,[
    ['Repository',x=>x.repository||repoName(x)||'—'],
    ['Class',x=>runnerClass(poolByID?.get(x.poolId))],
    ['Job',x=>x.jobName||'startup'],
    ['Result',x=>workerDisplayState(x)],
    ['CPU now',x=>!terminal.has(String(x.state||'').toLowerCase())&&x.usage?.updatedAt?usageCPU(x.usage.currentCpuMillis):'—'],
    ['CPU peak',x=>x.usage?.samples?usageCPU(x.usage.peakCpuMillis):'—'],
    ['RAM now',x=>!terminal.has(String(x.state||'').toLowerCase())&&x.usage?.updatedAt?usageRAM(x.usage.currentMemoryBytes):'—'],
    ['RAM peak',x=>x.usage?.samples?usageRAM(x.usage.peakMemoryBytes):'—'],
    ['Allocated',x=>(x.resources?.cpuMillis?cpuCores(x.resources.cpuMillis)+' CPU':'—')+' / '+(x.resources?.memoryBytes?gib(x.resources.memoryBytes)+' GiB':'—')],
    ['Updated',x=>fmtTime(workerTimestamp(x))],
    ['Failure',x=>x.failureReason||'']
  ])+'<div class="muted small workers-note">Historical and deleted worker records are hidden here. Diagnostics retains the full history.</div>';
}

async function loadAuth(){
  try{
    auth=await fetch('/auth/github/status').then(r=>r.json());
    csrf=auth.csrf||'';
    if(!auth.enabled){
      els.authstatus.textContent='GitHub browser sign-in is not configured on this RunnerHub instance.';
      els.account.textContent='';
      show(els.login,false);show(els.install,false);show(els.logout,false);show(els.legacy,true);
      return;
    }
    if(!auth.authenticated){
      els.authstatus.textContent='Sign in, then install RunnerHub on the repositories you want to use.';
      els.account.textContent='';
      show(els.login,true);show(els.install,false);show(els.logout,false);show(els.legacy,false);
      return;
    }
    els.authstatus.textContent='Connected repositories can run GitHub Actions on this RunnerHub fleet.';
    els.account.textContent='@'+auth.user;
    show(els.login,false);show(els.install,true);show(els.logout,true);show(els.legacy,false);
  }catch(e){message(e.message,'error')}
}

async function refresh(){
  await loadAuth();
  const canAPI=auth.authenticated||Boolean(els.token.value);
  if(!canAPI){
    els.hosts.innerHTML=els.pools.innerHTML=els.activeRunners.innerHTML=els.workers.innerHTML=els.audit.innerHTML='<span class="muted">Sign in to view.</span>';
    els.activeRunnerSummary.textContent='';
    els.workerSummary.textContent='';
    els.repos.innerHTML='';
    return;
  }
  try{
    const requests=[
      j('/api/v1/hosts'),
      j('/api/v1/pools'),
      j('/api/v1/workers'),
      j('/api/v1/workers/progress').catch(()=>[]),
      j('/api/v1/audit?limit=12'),
      j('/api/v1/scalesets/status'),
      j('/api/v1/runtime-health')
    ];
    if(auth.authenticated)requests.push(j('/api/v1/github/connected-repositories'));
    const results=await Promise.all(requests);
    const [h,p,w,progress,a,statuses,runtimeHealth,repos]=results;
    const listenerByPool=new Map((statuses||[]).map(x=>[x.poolId,x]));
    const runtimeByPool=new Map((runtimeHealth||[]).map(x=>[x.poolId,x]));
    const poolByID=new Map((p||[]).map(x=>[x.id,x]));
    const hostByID=new Map((h||[]).map(x=>[x.id,x]));
    const progressByWorker=new Map((progress||[]).map(x=>[x.workerId,x]));
    const recommendationPairs=await Promise.all((p||[]).map(async pool=>{
      try{return [pool.id,await j('/api/v1/pools/'+encodeURIComponent(pool.id)+'/recommendation')]}
      catch{return [pool.id,null]}
    }));
    const recommendationByPool=new Map(recommendationPairs);

    renderHosts(h);
    els.pools.innerHTML=tab(p,[
      ['Repository',x=>repoName(x)],
      ['Class',x=>runnerClass(x)],
      ['Request',x=>cpuCores(x.resources?.cpuMillis)+' cores / '+gib(x.resources?.memoryBytes)+' GiB'],
      ['Active',x=>{const r=recommendationByPool.get(x.id);return r?(r.capacity.activeWorkers+' / '+x.maxConcurrency):'—'}],
      ['Hosts',x=>recommendationByPool.get(x.id)?.capacity?.compatibleHosts??'—'],
      ['Queue',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.queue)],
      ['Provision',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.provision)],
      ['Register',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.registration)],
      ['Execute',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.execution)],
      ['Cleanup',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.cleanup)],
      ['CPU reserved',x=>reservedCPU(recommendationByPool.get(x.id)?.capacity)],
      ['RAM reserved',x=>reservedRAM(recommendationByPool.get(x.id)?.capacity)],
      ['Observed p95',x=>observedText(recommendationByPool.get(x.id))],
      ['Suggested size',x=>sizingText(recommendationByPool.get(x.id))],
      ['Auto-size',x=>x.autoTuneResources?'on':'off'],
      ['Can fit',x=>{const n=recommendationByPool.get(x.id)?.capacity?.additionalWorkers;return Number.isFinite(n)?'+'+n:'—'}],
      ['Bottleneck',x=>bottleneckLabel(recommendationByPool.get(x.id)?.capacity?.bottleneck)],
      ['Reason',x=>recommendationByPool.get(x.id)?.capacity?.reason||'—'],
      ['Recommend',x=>recommendationByPool.get(x.id)?.recommended??'—'],
      ['Listener',x=>listenerByPool.get(x.id)?.state||'pending'],
      ['Runtime',x=>runtimeByPool.get(x.id)?.state||'unknown']
    ]);
    renderActiveRunners(w,poolByID,hostByID,progressByWorker);
    renderWorkers(w,poolByID);
    els.audit.innerHTML=tab(a,[
      ['When',x=>fmtTime(x.createdAt)],
      ['Action',x=>x.action],
      ['Result',x=>x.result],
      ['Detail',x=>x.safeDetail||'']
    ]);
    if(auth.authenticated&&!els.repos.querySelector('.repo-config[open]'))renderRepos(repos||[],p||[],statuses||[],runtimeHealth||[],h||[]);
  }catch(e){message(e.message,'error')}
}

function resourceEditor(repo,pool){
  const connected=Boolean(pool);
  const cpu=pool?.resources?.cpuMillis?cpuCores(pool.resources.cpuMillis):2.5;
  const memory=pool?.resources?.memoryBytes?gib(pool.resources.memoryBytes):4;
  const max=pool?.maxConcurrency||2;
  const action=connected?'Update settings':'Connect runner';
  const poolAttr=pool?' data-pool-id="'+esc(pool.id)+'"':'';
  return '<details class="repo-config">'+
    '<summary>'+esc(connected?'Configure runner resources':'Connect')+'</summary>'+
    '<div class="resource-grid">'+
      '<label>CPU / runner<input data-field="cpu" type="number" min="0.25" step="0.25" value="'+esc(cpu)+'"></label>'+
      '<label>RAM / runner (GiB)<input data-field="memory" type="number" min="1" step="0.5" value="'+esc(memory)+'"></label>'+
      '<label>Max concurrent<input data-field="concurrency" type="number" min="1" step="1" value="'+esc(max)+'"></label>'+
    '</div>'+
    '<label class="check-row"><input data-field="autotune" type="checkbox"'+(pool?.autoTuneResources?' checked':'')+'> Auto-size CPU/RAM from observed job usage</label>'+
    '<div class="resource-note muted">Auto-size waits for at least 5 completed jobs, then uses p95 peak usage + 25% headroom. Limits apply to newly created runners.</div>'+
    '<button data-resource-action="'+(connected?'update':'connect')+'" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+poolAttr+'>'+esc(action)+'</button>'+
  '</details>';
}

function publisherEditor(repo,pool,status,runtime,hostAvailable,standardConnected){
  const enabled=isPublisherEnabled(pool);
  const cpu=pool?.resources?.cpuMillis?cpuCores(pool.resources.cpuMillis):2.5;
  const memory=pool?.resources?.memoryBytes?gib(pool.resources.memoryBytes):8;
  const max=pool?.maxConcurrency||2;
  const listener=enabled?(status?.state||'pending'):'disabled';
  const runtimeState=enabled?(runtime?.state||'unknown'):'disabled';
  let badge='<span class="badge neutral">Publisher disabled</span>';
  if(enabled){
    const listenerClass=listener==='ready'?'':' warn';
    const runtimeClass=(runtimeState==='healthy'||runtimeState==='active')?'':' warn';
    badge='<span class="badge'+listenerClass+'">Publisher '+esc(listener)+'</span><span class="badge'+runtimeClass+'">Runtime '+esc(runtimeState)+'</span>';
  }else if(!hostAvailable){
    badge='<span class="badge warn">Publisher unavailable</span>';
  }
  let note='Docker builds, container actions, services, and image publishing use <code>runs-on: runnerhub-publisher</code>. Publisher jobs get a private Docker-in-Docker daemon; the NAS Docker socket is not mounted into workflow jobs.';
  if(!standardConnected)note='Connect the standard runner first, then enable publisher runners for this repository.';
  else if(!hostAvailable&&!enabled)note+=' Enable privileged publisher support in the RunnerHub deployment first (<code>allow-privileged-workers: true</code>).';
  const controls=standardConnected?(hostAvailable||enabled):false;
  const fields='<div class="resource-grid">'+
      '<label>Publisher CPU<input data-publisher-field="cpu" type="number" min="0.25" step="0.25" value="'+esc(cpu)+'"></label>'+
      '<label>Publisher RAM (GiB)<input data-publisher-field="memory" type="number" min="1" step="0.5" value="'+esc(memory)+'"></label>'+
      '<label>Max publishers<input data-publisher-field="concurrency" type="number" min="1" step="1" value="'+esc(max)+'"></label>'+
    '</div>'+
    '<label class="check-row"><input data-publisher-field="autotune" type="checkbox"'+(pool?.autoTuneResources?' checked':'')+'> Auto-size publisher CPU/RAM from observed jobs</label>'+
    '<div class="resource-note muted">Max publishers is an upper bound. Auto-size waits for at least 5 completed publisher jobs and keeps host compatibility checks.</div>';
  let buttons='';
  if(enabled){
    buttons='<div class="publisher-actions"><button data-publisher-action="update" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Save publisher settings</button><button class="secondary" data-publisher-action="disable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Disable publisher</button></div>';
  }else{
    buttons='<button data-publisher-action="enable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+(controls?'':' disabled')+'>Enable publisher runner</button>';
  }
  return '<details class="repo-config publisher-config">'+
    '<summary>Publisher runner</summary>'+
    '<div class="repo-status">'+badge+'</div>'+
    '<div class="resource-note">'+note+'</div>'+
    fields+buttons+
  '</details>';
}

function runnerProfileEditor(repo,profiles,standardConnected){
  const active=(profiles||[]).filter(p=>Number(p.githubInstallationId)>0);
  const existing=(profiles||[]).map(p=>{
    const tags=Object.keys(p.hostSelector||{}).join(', ')||'—';
    const enabled=Number(p.githubInstallationId)>0;
    const mode=p.isolation==='container-dind'?'Docker-capable':'standard';
    return '<div class="profile-row"><div><code>runs-on: '+esc(p.scaleSetName)+'</code><div class="muted small">machines: '+esc(tags)+' · '+esc(mode)+' · '+esc(cpuCores(p.resources?.cpuMillis))+' CPU · '+esc(gib(p.resources?.memoryBytes))+' GiB</div></div>'+
      (enabled?'<button class="secondary compact" data-profile-action="disable" data-profile-name="'+esc(p.scaleSetName)+'" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Disable</button>':'<span class="badge neutral">disabled</span>')+
    '</div>';
  }).join('');
  const disabled=standardConnected?'':' disabled';
  return '<details class="repo-config profile-config">'+
    '<summary>Custom runner profiles'+(active.length?' ('+active.length+')':'')+'</summary>'+
    '<div class="resource-note muted">Tags are hard placement constraints. Use host priority for normal scheduling preference; use a custom profile when a workflow must explicitly target a machine class such as <code>runnerhub-fast</code>. Docker-capable profiles reserve two host CPU/RAM envelopes per runner (runner + private DinD sidecar).</div>'+
    (existing?'<div class="profile-list">'+existing+'</div>':'')+
    '<div class="resource-grid">'+
      '<label>Profile name<input data-profile-field="name" placeholder="fast"'+disabled+'></label>'+
      '<label>Required machine tags<input data-profile-field="tags" placeholder="fast"'+disabled+'></label>'+
      '<label>Mode<select data-profile-field="isolation"'+disabled+'><option value="container-trusted">Standard</option><option value="container-dind">Docker-capable</option></select></label>'+
      '<label>CPU / runner<input data-profile-field="cpu" type="number" min="0.25" step="0.25" value="2.5"'+disabled+'></label>'+
      '<label>RAM / runner (GiB)<input data-profile-field="memory" type="number" min="1" step="0.5" value="4"'+disabled+'></label>'+
      '<label>Max concurrent<input data-profile-field="concurrency" type="number" min="1" step="1" value="2"'+disabled+'></label>'+
    '</div>'+
    '<label class="check-row"><input data-profile-field="autotune" type="checkbox"'+disabled+'> Auto-size CPU/RAM from observed jobs</label>'+
    '<button data-profile-action="enable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+disabled+'>Create / update profile</button>'+
  '</details>';
}

function renderRepos(repos,pools,statuses,runtimeHealth,hosts){
  const poolsByRepo=new Map();
  (pools||[]).filter(p=>p.scope?.kind==='repository').forEach(p=>{
    const key=(p.scope.owner+'/'+p.scope.repository).toLowerCase();
    const entry=poolsByRepo.get(key)||{standard:null,publisher:null,profiles:[]};
    if(isPublisherPool(p))entry.publisher=p;
    else if(isStandardPool(p))entry.standard=p;
    else entry.profiles.push(p);
    poolsByRepo.set(key,entry);
  });
  const statusByPool=new Map((statuses||[]).map(s=>[s.poolId,s]));
  const runtimeByPool=new Map((runtimeHealth||[]).map(s=>[s.poolId,s]));
  const publisherHostAvailable=(hosts||[]).some(h=>h.state==='online'&&h.tags?.['isolation.container-dind']==='true');
  if(!repos.length){
    els.repos.innerHTML='<div class="empty">No repositories are available through the GitHub App installation. Use <strong>Manage repositories</strong> to grant access.</div>';
    return;
  }

  const sorted=[...repos].sort((a,b)=>{
    const ac=poolsByRepo.get(a.fullName.toLowerCase())?.standard?1:0;
    const bc=poolsByRepo.get(b.fullName.toLowerCase())?.standard?1:0;
    if(ac!==bc)return bc-ac;
    return a.fullName.localeCompare(b.fullName);
  });

  els.repos.innerHTML=sorted.map(repo=>{
    const entry=poolsByRepo.get(repo.fullName.toLowerCase())||{standard:null,publisher:null,profiles:[]};
    const pool=entry.standard;
    const publisher=entry.publisher;
    const status=pool?statusByPool.get(pool.id):null;
    const runtime=pool?runtimeByPool.get(pool.id):null;
    const publisherStatus=publisher?statusByPool.get(publisher.id):null;
    const publisherRuntime=publisher?runtimeByPool.get(publisher.id):null;
    const privacy=repo.private?'Private':'Public';
    let statusHtml='';
    let detail='';

    if(pool){
      const listener=status?.state||'pending';
      const runtimeState=runtime?.state||'unknown';
      const listenerClass=listener==='ready'?'':' warn';
      const listenerText=listener==='ready'?'Listener ready':'Listener '+listener;
      let runtimeClass=' warn';
      let runtimeText='Runtime unverified';
      if(runtimeState==='healthy'){runtimeClass='';runtimeText='Runtime healthy'}
      else if(runtimeState==='active'){runtimeClass='';runtimeText='Runtime active'}
      else if(runtimeState==='degraded'){runtimeClass=' warn';runtimeText='Runtime degraded'}
      statusHtml='<div class="repo-status"><span class="badge'+listenerClass+'">'+esc(listenerText)+'</span><span class="badge'+runtimeClass+'">'+esc(runtimeText)+'</span></div>';
      detail='<div class="resource-summary">'+esc(cpuCores(pool.resources?.cpuMillis))+' CPU · '+esc(gib(pool.resources?.memoryBytes))+' GiB RAM · max '+esc(pool.maxConcurrency)+'</div>';
      if(status?.detail&&listener!=='ready')detail+='<div class="runtime-detail muted">'+esc(status.detail)+'</div>';
      if(runtimeState==='degraded'&&runtime?.lastFailureReason)detail+='<div class="runtime-detail muted">Last failure: '+esc(runtime.lastFailureReason)+'</div>';
    }else if(!repo.private){
      statusHtml='<div class="repo-status"><span class="badge warn">Public unsupported</span></div>';
    }else{
      statusHtml='<div class="repo-status"><span class="badge neutral">Not connected</span></div>';
    }

    return '<article class="repo'+(pool?' connected':'')+'">'+
      '<div class="repo-row">'+
        '<div class="repo-title"><div><div class="repo-name">'+esc(repo.fullName)+'</div><div class="repo-meta muted">'+esc(privacy)+' · '+esc(repo.defaultBranch||'')+'</div></div></div>'+
        statusHtml+detail+
      '</div>'+
      (repo.private?resourceEditor(repo,pool):'')+
      (repo.private?runnerProfileEditor(repo,entry.profiles,Boolean(pool)):'')+
      (repo.private?publisherEditor(repo,publisher,publisherStatus,publisherRuntime,publisherHostAvailable,Boolean(pool)):'')+
      '</article>';
  }).join('');
}

async function scanMachineHostKey(){
  const button=document.getElementById('machine-scan-fingerprint');
  const address=document.getElementById('machine-address').value.trim();
  if(!address)return message('Enter the machine IP / hostname before scanning its SSH host key.','error');
  button.disabled=true;
  try{
    message('Scanning SSH host keys for '+address+'…');
    const result=await j('/api/v1/hosts/ssh-fingerprints',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({address})});
    const fingerprints=Array.isArray(result?.fingerprints)?result.fingerprints:[];
    if(!fingerprints.length)throw new Error('No SSH host-key fingerprints were returned.');
    if(fingerprints.length===1)document.getElementById('machine-host-fingerprint').value=fingerprints[0];
    message('Observed SSH fingerprint'+(fingerprints.length===1?'':'s')+': '+fingerprints.join(', ')+'. Compare with a trusted console, then paste the matching SHA256 fingerprint before adding the machine.','success');
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function addMachine(){
  const button=document.getElementById('add-machine');
  const body={
    address:document.getElementById('machine-address').value.trim(),
    user:document.getElementById('machine-user').value.trim(),
    name:document.getElementById('machine-name').value.trim(),
    tags:document.getElementById('machine-tags').value.split(',').map(x=>x.trim()).filter(Boolean),
    priority:Number(document.getElementById('machine-priority').value||0),
    hostKeyFingerprint:document.getElementById('machine-host-fingerprint').value.trim(),
    password:document.getElementById('machine-password').value,
    sudoPassword:document.getElementById('machine-sudo-password').value,
    privateKey:document.getElementById('machine-private-key').value,
    registryUsername:document.getElementById('machine-registry-username').value.trim(),
    registryPassword:document.getElementById('machine-registry-password').value,
    installDocker:document.getElementById('machine-install-docker').checked,
    allowPrivilegedWorkers:document.getElementById('machine-allow-privileged').checked
  };
  if(!body.address||!body.user||!body.name)return message('Machine address, SSH user, and RunnerHub name are required.','error');
  if(!body.hostKeyFingerprint)return message('Verify and enter the SSH host key fingerprint before adding the machine.','error');
  button.disabled=true;
  try{
    message('Connecting to '+body.address+' and enrolling '+body.name+'…');
    const host=await j('/api/v1/hosts/bootstrap',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    document.getElementById('machine-host-fingerprint').value='';
    document.getElementById('machine-password').value='';
    document.getElementById('machine-sudo-password').value='';
    document.getElementById('machine-private-key').value='';
    document.getElementById('machine-registry-password').value='';
    message(host.name+' is online and available for RunnerHub CI.','success');
    await refresh();
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function saveHostTags(button){
  const id=button.dataset.hostTags;
  const input=els.hosts.querySelector('[data-host-tag-input="'+CSS.escape(id)+'"]');
  const priorityInput=els.hosts.querySelector('[data-host-priority-input="'+CSS.escape(id)+'"]');
  const priority=Number(priorityInput?.value||0);
  if(!Number.isInteger(priority)||priority < -1000||priority > 1000)return message('Host priority must be an integer between -1000 and 1000.','error');
  const tags={};
  (input?.value||'').split(',').map(x=>x.trim()).filter(Boolean).forEach(tag=>{tags[tag]='true'});
  button.disabled=true;
  try{
    await j('/api/v1/hosts/'+encodeURIComponent(id),{method:'PATCH',headers:{'content-type':'application/json'},body:JSON.stringify({tags,priority})});
    message('Machine scheduling settings updated.','success');
    await refresh();
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function saveRunnerProfile(button){
  const panel=button.closest('.profile-config');
  const enabled=button.dataset.profileAction!=='disable';
  const body={installationId:Number(button.dataset.installation),repository:button.dataset.repo,enabled};
  if(enabled){
    body.name=panel.querySelector('[data-profile-field="name"]').value.trim();
    body.tags=panel.querySelector('[data-profile-field="tags"]').value.split(',').map(x=>x.trim()).filter(Boolean);
    body.isolation=panel.querySelector('[data-profile-field="isolation"]').value;
    const cpu=Number(panel.querySelector('[data-profile-field="cpu"]').value);
    const memory=Number(panel.querySelector('[data-profile-field="memory"]').value);
    const concurrency=Number(panel.querySelector('[data-profile-field="concurrency"]').value);
    if(!body.name)return message('Profile name is required.','error');
    if(!body.tags.length)return message('At least one required machine tag is required.','error');
    if(!Number.isFinite(cpu)||cpu<=0||!Number.isFinite(memory)||memory<=0||!Number.isInteger(concurrency)||concurrency<1)return message('Profile CPU, RAM, and concurrency must be valid.','error');
    body.resources={cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824),diskBytes:body.isolation==='container-dind'?42949672960:21474836480};
    body.maxConcurrency=concurrency;
    body.autoTuneResources=panel.querySelector('[data-profile-field="autotune"]').checked;
  }else{
    body.name=button.dataset.profileName;
  }
  button.disabled=true;
  try{
    await j('/api/v1/github/profile',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    const name=String(body.name).startsWith('runnerhub-')?body.name:'runnerhub-'+body.name;
    message(enabled?'Profile '+name+' configured. Use runs-on: '+name+'.':'Profile '+name+' disabled.','success');
    panel.open=false;
    await refresh();
  }catch(e){message(e.message,'error');button.disabled=false}
}

async function saveRepositoryResources(button){
  const panel=button.closest('.repo-config');
  const cpu=Number(panel.querySelector('[data-field="cpu"]').value);
  const memory=Number(panel.querySelector('[data-field="memory"]').value);
  const concurrency=Number(panel.querySelector('[data-field="concurrency"]').value);
  const isolation='container-trusted';
  if(!Number.isFinite(cpu)||cpu<=0)return message('CPU must be greater than 0.','error');
  if(!Number.isFinite(memory)||memory<=0)return message('RAM must be greater than 0.','error');
  if(!Number.isInteger(concurrency)||concurrency<1)return message('Max concurrent runners must be at least 1.','error');
  button.disabled=true;
  const resources={cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824)};
  const autoTuneResources=panel.querySelector('[data-field="autotune"]').checked;
  try{
    if(button.dataset.resourceAction==='update'){
      message('Updating '+button.dataset.repo+' runner resources…');
      await j('/api/v1/pools/'+encodeURIComponent(button.dataset.poolId)+'/resources',{
        method:'PATCH',
        headers:{'content-type':'application/json'},
        body:JSON.stringify({resources,maxConcurrency:concurrency,isolation,autoTuneResources})
      });
      message(button.dataset.repo+' runner settings updated. New runners will use the new limits.','success');
    }else{
      message('Connecting '+button.dataset.repo+'…');
      await j('/api/v1/github/connect',{
        method:'POST',
        headers:{'content-type':'application/json'},
        body:JSON.stringify({installationId:Number(button.dataset.installation),repository:button.dataset.repo,scaleSetName:'runnerhub',maxConcurrency:concurrency,isolation,resources,autoTuneResources})
      });
      message(button.dataset.repo+' connection requested. Waiting for the GitHub scale-set listener to become ready.','success');
    }
    panel.open=false;
    await refresh();
  }catch(e){message(e.message,'error');button.disabled=false}
}

async function savePublisher(button){
  const panel=button.closest('.publisher-config');
  const enabled=button.dataset.publisherAction!=='disable';
  const body={installationId:Number(button.dataset.installation),repository:button.dataset.repo,enabled};
  if(enabled){
    const cpu=Number(panel.querySelector('[data-publisher-field="cpu"]').value);
    const memory=Number(panel.querySelector('[data-publisher-field="memory"]').value);
    const concurrency=Number(panel.querySelector('[data-publisher-field="concurrency"]').value);
    if(!Number.isFinite(cpu)||cpu<=0)return message('Publisher CPU must be greater than 0.','error');
    if(!Number.isFinite(memory)||memory<=0)return message('Publisher RAM must be greater than 0.','error');
    if(!Number.isInteger(concurrency)||concurrency<1)return message('Publisher concurrency must be at least 1.','error');
    body.resources={cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824),diskBytes:42949672960};
    body.maxConcurrency=concurrency;
    body.autoTuneResources=panel.querySelector('[data-publisher-field="autotune"]').checked;
  }
  button.disabled=true;
  try{
    message((enabled?'Configuring ':'Disabling ')+button.dataset.repo+' publisher runner…');
    await j('/api/v1/github/publisher',{
      method:'POST',
      headers:{'content-type':'application/json'},
      body:JSON.stringify(body)
    });
    message(button.dataset.repo+(enabled?' publisher runner configured. Use runs-on: runnerhub-publisher for Docker/image jobs.':' publisher runner disabled.'),'success');
    panel.open=false;
    await refresh();
  }catch(e){message(e.message,'error');button.disabled=false}
}

async function logout(){
  try{
    await j('/auth/logout',{method:'POST'});
    csrf='';auth={enabled:true,authenticated:false};
    await refresh();
  }catch(e){message(e.message,'error')}
}

async function createPool(){
  try{
    const cpu=Number(document.getElementById('pool-cpu').value);
    const memory=Number(document.getElementById('pool-memory').value);
    const maxConcurrency=Number(document.getElementById('pool-concurrency').value);
    if(!Number.isFinite(cpu)||cpu<=0)return message('CPU must be greater than 0.','error');
    if(!Number.isFinite(memory)||memory<=0)return message('RAM must be greater than 0.','error');
    if(!Number.isInteger(maxConcurrency)||maxConcurrency<1)return message('Max concurrent runners must be at least 1.','error');
    const body={
      name:document.getElementById('pname').value,
      scope:{kind:'repository',owner:document.getElementById('owner').value,repository:document.getElementById('repo').value},
      labels:document.getElementById('labels').value.split(',').map(x=>x.trim()).filter(Boolean),
      isolation:document.getElementById('isolation').value,
      resources:{cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824),diskBytes:21474836480},
      minIdle:0,maxConcurrency,runnerGroupId:Number(document.getElementById('group').value),
      image:document.getElementById('image').value
    };
    await j('/api/v1/pools',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    message('Pool created.','success');refresh();
  }catch(e){message(e.message,'error')}
}

refresh();
setInterval(refresh,10000);
