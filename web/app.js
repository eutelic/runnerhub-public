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
  const button=e.target.closest('button[data-repo]');
  if(button)connectRepository(button);
});

function esc(value){return String(value??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]))}
function show(el,on){el.classList.toggle('hidden',!on)}
function message(text,kind=''){els.msg.className=kind;els.msg.textContent=text||''}

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
    els.authstatus.textContent='Choose a private repository to connect. GitHub controls which repositories RunnerHub can see.';
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
    els.hosts.innerHTML=tab(h,[['Name',x=>x.name],['State',x=>x.state],['Provider',x=>x.provider],['CPU',x=>x.capacity?.cpuMillis?x.capacity.cpuMillis/1000+' cores':'unknown']]);
    els.pools.innerHTML=tab(p,[['Name',x=>x.name],['Repository',x=>x.scope?.repository?x.scope.owner+'/'+x.scope.repository:x.scope?.owner||''],['runs-on',x=>(x.labels||[]).join(', ')],['Listener',x=>runtime.get(x.id)?.state||'pending'],['Max',x=>x.maxConcurrency]]);
    const visibleWorkers=(w||[]).filter(x=>x.state!=='deleted');
    els.workers.innerHTML=tab(visibleWorkers,[['Runner',x=>x.runnerName],['State',x=>x.state],['Host',x=>x.hostId],['Repo',x=>x.repository||''],['Job',x=>x.jobName||'']]);
    els.audit.innerHTML=tab(a,[['When',x=>x.createdAt],['Action',x=>x.action],['Result',x=>x.result],['Detail',x=>x.safeDetail||'']]);
    if(auth.authenticated)renderRepos(repos||[],p||[],statuses||[]);
  }catch(e){message(e.message,'error')}
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
    let action='';
    let detail='';
    if(pool){
      const state=status?.state||'pending';
      if(state==='ready')action='<span class="badge">Ready</span>';
      else if(state==='error')action='<span class="badge warn">Error</span>';
      else if(state==='starting')action='<span class="badge warn">Starting</span>';
      else action='<span class="badge warn">Pending</span>';
      if(status?.detail)detail='<div class="muted">'+esc(status.detail)+'</div>';
    }else if(repo.private){
      action='<button data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Connect</button>';
    }else{
      action='<span class="badge warn">Public unsupported</span>';
    }
    return '<article class="repo">'+
      '<div><strong>'+esc(repo.fullName)+'</strong><div class="muted">'+esc(privacy)+' · '+esc(repo.defaultBranch||'')+'</div>'+detail+'</div>'+
      action+
      '</article>';
  }).join('');
}

async function connectRepository(button){
  button.disabled=true;
  message('Connecting '+button.dataset.repo+'…');
  try{
    await j('/api/v1/github/connect',{
      method:'POST',
      headers:{'content-type':'application/json'},
      body:JSON.stringify({installationId:Number(button.dataset.installation),repository:button.dataset.repo,scaleSetName:'runnerhub',maxConcurrency:2})
    });
    message(button.dataset.repo+' connection requested. Waiting for the GitHub scale-set listener to become ready.','success');
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
    const body={
      name:document.getElementById('pname').value,
      scope:{kind:'repository',owner:document.getElementById('owner').value,repository:document.getElementById('repo').value},
      labels:document.getElementById('labels').value.split(',').map(x=>x.trim()).filter(Boolean),
      isolation:document.getElementById('isolation').value,
      resources:{cpuMillis:2000,memoryBytes:4294967296,diskBytes:0},
      minIdle:0,maxConcurrency:2,runnerGroupId:Number(document.getElementById('group').value),
      image:document.getElementById('image').value
    };
    await j('/api/v1/pools',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    message('Pool created.','success');refresh();
  }catch(e){message(e.message,'error')}
}

refresh();
setInterval(refresh,10000);
