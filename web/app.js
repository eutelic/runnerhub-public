const els={
  token:document.getElementById('apitoken'),
  hosts:document.getElementById('hosts'),
  pools:document.getElementById('pools'),
  workers:document.getElementById('workers'),
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
els.logout.addEventListener('click',logout);
els.repos.addEventListener('click',e=>{
  const button=e.target.closest('button[data-resource-action]');
  if(button)saveRepositoryResources(button);
});

function esc(value){return String(value??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]))}
function show(el,on){el.classList.toggle('hidden',!on)}
function message(text,kind=''){els.msg.className=kind;els.msg.textContent=text||''}
function gib(bytes){return bytes?Number((bytes/1073741824).toFixed(2)):0}
function cpuCores(millis){return millis?Number((millis/1000).toFixed(2)):0}

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
    els.authstatus.textContent='Choose a private repository and configure the resources each runner may use.';
    els.account.textContent='@'+auth.user;
    show(els.login,false);show(els.install,true);show(els.logout,true);show(els.legacy,false);
  }catch(e){message(e.message,'error')}
}

async function refresh(){
  await loadAuth();
  const canAPI=auth.authenticated||Boolean(els.token.value);
  if(!canAPI){
    els.hosts.innerHTML=els.pools.innerHTML=els.workers.innerHTML=els.audit.innerHTML='<span class="muted">Sign in to view.</span>';
    els.repos.innerHTML='';
    return;
  }
  try{
    const requests=[
      j('/api/v1/hosts'),
      j('/api/v1/pools'),
      j('/api/v1/workers'),
      j('/api/v1/audit?limit=20'),
      j('/api/v1/scalesets/status')
    ];
    if(auth.authenticated)requests.push(j('/api/v1/github/connected-repositories'));
    const results=await Promise.all(requests);
    const [h,p,w,a,statuses,repos]=results;
    const runtime=new Map((statuses||[]).map(x=>[x.poolId,x]));
    els.hosts.innerHTML=tab(h,[['Name',x=>x.name],['State',x=>x.state],['Provider',x=>x.provider],['CPU',x=>x.capacity?.cpuMillis?cpuCores(x.capacity.cpuMillis)+' cores':'unknown'],['Memory',x=>x.capacity?.memoryBytes?gib(x.capacity.memoryBytes)+' GiB':'unknown']]);
    els.pools.innerHTML=tab(p,[['Name',x=>x.name],['Repository',x=>x.scope?.repository?x.scope.owner+'/'+x.scope.repository:x.scope?.owner||''],['runs-on',x=>(x.labels||[]).join(', ')],['CPU',x=>cpuCores(x.resources?.cpuMillis)+' cores'],['Memory',x=>gib(x.resources?.memoryBytes)+' GiB'],['Listener',x=>runtime.get(x.id)?.state||'pending'],['Max',x=>x.maxConcurrency]]);
    const visibleWorkers=(w||[]).filter(x=>x.state!=='deleted');
    els.workers.innerHTML=tab(visibleWorkers,[['Runner',x=>x.runnerName],['State',x=>x.state],['Host',x=>x.hostId],['Repo',x=>x.repository||''],['Job',x=>x.jobName||'']]);
    els.audit.innerHTML=tab(a,[['When',x=>x.createdAt],['Action',x=>x.action],['Result',x=>x.result],['Detail',x=>x.safeDetail||'']]);
    if(auth.authenticated&&!els.repos.querySelector('.repo-config[open]'))renderRepos(repos||[],p||[],statuses||[]);
  }catch(e){message(e.message,'error')}
}

function resourceEditor(repo,pool){
  const connected=Boolean(pool);
  const cpu=pool?.resources?.cpuMillis?cpuCores(pool.resources.cpuMillis):2.5;
  const memory=pool?.resources?.memoryBytes?gib(pool.resources.memoryBytes):4;
  const max=pool?.maxConcurrency||2;
  const isolation=pool?.isolation||'container-trusted';
  const action=connected?'Update settings':'Connect runner';
  const poolAttr=pool?' data-pool-id="'+esc(pool.id)+'"':'';
  return '<details class="repo-config">'+
    '<summary>'+esc(connected?'Configure runner resources':'Connect')+'</summary>'+
    '<div class="resource-grid">'+
      '<label>CPU / runner<input data-field="cpu" type="number" min="0.25" step="0.25" value="'+esc(cpu)+'"></label>'+
      '<label>RAM / runner (GiB)<input data-field="memory" type="number" min="1" step="0.5" value="'+esc(memory)+'"></label>'+
      '<label>Max concurrent<input data-field="concurrency" type="number" min="1" step="1" value="'+esc(max)+'"></label>'+
      '<label>Isolation<select data-field="isolation"><option value="container-trusted"'+(isolation==='container-trusted'?' selected':'')+'>Container</option><option value="container-dind"'+(isolation==='container-dind'?' selected':'')+'>Container + Docker</option></select></label>'+
    '</div>'+
    '<div class="resource-note muted">Limits apply to newly created runners. CPU may be fractional.</div>'+
    '<button data-resource-action="'+(connected?'update':'connect')+'" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+poolAttr+'>'+esc(action)+'</button>'+
  '</details>';
}

function renderRepos(repos,pools,statuses){
  const poolsByRepo=new Map((pools||[])
    .filter(p=>p.scope?.kind==='repository')
    .map(p=>[(p.scope.owner+'/'+p.scope.repository).toLowerCase(),p]));
  const statusByPool=new Map((statuses||[]).map(s=>[s.poolId,s]));
  if(!repos.length){
    els.repos.innerHTML='<div class="empty">No repositories are available through the GitHub App installation. Use <strong>Install / manage repositories</strong> to grant access.</div>';
    return;
  }
  els.repos.innerHTML=repos.map(repo=>{
    const pool=poolsByRepo.get(repo.fullName.toLowerCase());
    const status=pool?statusByPool.get(pool.id):null;
    const privacy=repo.private?'Private':'Public';
    let state='';
    let detail='';
    if(pool){
      const current=status?.state||'pending';
      if(current==='ready')state='<span class="badge">Ready</span>';
      else if(current==='error')state='<span class="badge warn">Error</span>';
      else if(current==='starting')state='<span class="badge warn">Starting</span>';
      else state='<span class="badge warn">Pending</span>';
      detail='<div class="resource-summary">'+esc(cpuCores(pool.resources?.cpuMillis))+' CPU · '+esc(gib(pool.resources?.memoryBytes))+' GiB · max '+esc(pool.maxConcurrency)+'</div>';
      if(status?.detail)detail+='<div class="muted">'+esc(status.detail)+'</div>';
    }else if(!repo.private){
      state='<span class="badge warn">Public unsupported</span>';
    }
    return '<article class="repo">'+
      '<div class="repo-row"><div><strong>'+esc(repo.fullName)+'</strong><div class="muted">'+esc(privacy)+' · '+esc(repo.defaultBranch||'')+'</div>'+detail+'</div>'+state+'</div>'+
      (repo.private?resourceEditor(repo,pool):'')+
      '</article>';
  }).join('');
}

async function saveRepositoryResources(button){
  const panel=button.closest('.repo-config');
  const cpu=Number(panel.querySelector('[data-field="cpu"]').value);
  const memory=Number(panel.querySelector('[data-field="memory"]').value);
  const concurrency=Number(panel.querySelector('[data-field="concurrency"]').value);
  const isolation=panel.querySelector('[data-field="isolation"]').value;
  if(!Number.isFinite(cpu)||cpu<=0)return message('CPU must be greater than 0.','error');
  if(!Number.isFinite(memory)||memory<=0)return message('RAM must be greater than 0.','error');
  if(!Number.isInteger(concurrency)||concurrency<1)return message('Max concurrent runners must be at least 1.','error');
  button.disabled=true;
  const resources={cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824)};
  try{
    if(button.dataset.resourceAction==='update'){
      message('Updating '+button.dataset.repo+' runner resources…');
      await j('/api/v1/pools/'+encodeURIComponent(button.dataset.poolId)+'/resources',{
        method:'PATCH',
        headers:{'content-type':'application/json'},
        body:JSON.stringify({resources,maxConcurrency:concurrency,isolation})
      });
      message(button.dataset.repo+' runner settings updated. New runners will use the new limits.','success');
    }else{
      message('Connecting '+button.dataset.repo+'…');
      await j('/api/v1/github/connect',{
        method:'POST',
        headers:{'content-type':'application/json'},
        body:JSON.stringify({installationId:Number(button.dataset.installation),repository:button.dataset.repo,scaleSetName:'runnerhub',maxConcurrency:concurrency,isolation,resources})
      });
      message(button.dataset.repo+' connection requested. Waiting for the GitHub scale-set listener to become ready.','success');
    }
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
