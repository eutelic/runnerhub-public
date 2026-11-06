const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require('node:path').join(__dirname,'app.js'),'utf8');
assert.match(source,/initPageNav\(\);\s*window\.addEventListener\('popstate',[\s\S]*?\);\s*renderMachineBootstrap\(\);\s*refresh\(\);\s*refreshMachineOperation\(\);/);
const testSource = source.replace(/initPageNav\(\);[\s\S]*$/, 'initPageNav();renderMachineBootstrap();');

function createDashboard(endpoints = {}, storage = new Map(), session = new Map()) {
  const elements = new Map();
  const element = id => {
    if (!elements.has(id)) {
      const classes = new Set();
      elements.set(id,{
        id, value:'', checked:true, innerHTML:'', textContent:'', className:'', dataset:{},
        disabled:false, handlers:{}, classList:{
          toggle(name,on){if(on)classes.add(name);else classes.delete(name)},
          contains(name){return classes.has(name)}
        },
        addEventListener(name,handler){this.handlers[name]=handler},
        focus(){},
        querySelector(){return null}, querySelectorAll(){return []}, contains(){return false}
      });
    }
    return elements.get(id);
  };
  const calls = [];
  const requestOptions = new Map();
  const document = {
    activeElement:{tagName:'BODY'},
    hidden:false,
    getElementById:element, querySelector(){return null}, querySelectorAll(){return []},
    body:{classList:{add(){}}}
  };
  const location={hostname:'localhost',origin:'http://localhost:8788',pathname:'/',hash:'',search:''};
  const urlChanges=[];
  const history={
    pushState(){},
    replaceState(_state,_title,url){
      urlChanges.push(url);
      const [pathAndQuery,fragment='']=url.split('#');
      const q=pathAndQuery.indexOf('?');
      location.pathname=q>=0?pathAndQuery.slice(0,q):pathAndQuery;
      location.search=q>=0?pathAndQuery.slice(q):'';
      location.hash=fragment?'#'+fragment:'';
    }
  };
  const ctx = vm.createContext({
    document,window:{addEventListener(){},confirm(){return true},prompt(){return 'https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/124571922351'}},location,history,sessionStorage:{
      getItem(key){return key==='runnerhub-token'?'test-token':(session.get(key)||null)},
      setItem(key,value){session.set(key,value)},removeItem(key){session.delete(key)}
    },localStorage:{getItem(key){return storage.get(key)||null},setItem(key,value){storage.set(key,value)}},
    CSS:{escape(text){return text}},
    fetch:async (path,options={})=>{
      calls.push(path);
      requestOptions.set(path,options);
      if(path==='/auth/github/status')return {json:async()=>({enabled:true,authenticated:true,csrf:'csrf',user:'tester'})};
      let value=endpoints[path]??[];
      if(value instanceof Error)throw value;
      return {ok:true,text:async()=>JSON.stringify(value)};
    },
    console,Date,Number,Map,Set,URLSearchParams
  });
  vm.runInContext(testSource,ctx,{filename:'app.js'});
  return {ctx,elements,element,document,calls,requestOptions,endpoints,storage,session,location,urlChanges};
}

test('pool queue p95 shows sample coverage and never disguises missing data as zero wait',()=>{
  const {ctx}=createDashboard();
  assert.equal(vm.runInContext('poolRecentQueueText(null)',ctx),'—');
  assert.equal(vm.runInContext('poolRecentQueueText({samples:0,startedJobs:3,p95Seconds:null})',ctx),'— · 0/3');
  assert.equal(vm.runInContext('poolRecentQueueText({samples:2,startedJobs:3,p95Seconds:90})',ctx),'1.5m · 2/3');
  assert.equal(vm.runInContext('poolRecentQueueText({samples:1,startedJobs:1,p95Seconds:0})',ctx),'0.00s · 1/1');
});

test('deployer dashboard shows maintenance and remaining CI workers',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext("renderDeployerStatus({available:true,configured:true,healthy:true,deployerApp:'runnerhub-deployer'},{paused:true,activeWorkers:2,phase:'draining'})",ctx);
  assert.match(element('deployer-summary').textContent,/Draining CI/);
  assert.match(element('deployer-status').innerHTML,/new CI jobs are paused/);
  assert.match(element('deployer-status').innerHTML,/2 active worker/);
  vm.runInContext("renderDeployerStatus({available:true,configured:true,healthy:true},{paused:false,activeWorkers:0,phase:'running'})",ctx);
  assert.doesNotMatch(element('deployer-status').innerHTML,/new CI jobs are paused/);
});

test('fleet overview identifies bad listener and zero online hosts with actionable links',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext(`renderFleetOverview(
    [{name:'compute',state:'offline'}],
    [{id:'p1',scope:{owner:'acme',repository:'build'},githubInstallationId:42,scaleSetName:'runnerhub',maxConcurrency:4}],
    [{state:'busy',resources:{cpuMillis:2000}}],
    [{poolId:'p1',state:'backing_off'}],
    [{poolId:'p1',state:'degraded'}]
  )`,ctx);
  const html=element('fleet-overview').innerHTML;
  assert.match(html,/Hosts online/);
  assert.match(html,/No machines online/);
  assert.match(html,/listener backing_off/);
  assert.match(html,/Open diagnostics|Inspect readiness/);
});

test('empty Active runners is not mistaken for an empty GitHub queue',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext('renderActiveRunners([],new Map(),new Map(),new Map())',ctx);
  assert.ok(element('active-runners').innerHTML.includes('GitHub may still have queued jobs'));
  assert.ok(!element('active-runners').innerHTML.includes('No queued jobs'));
});

test('GitHub queue summary counts only observed RunnerHub labels and deduplicates workflow runs',()=>{
  const {ctx}=createDashboard();
  ctx.sampleQueue={checkedRuns:3,truncated:true,jobs:[
    {id:1,runId:10,status:'queued',labels:['runnerhub']},
    {id:2,runId:10,status:'queued',labels:['runnerhub-publisher']},
    {id:3,runId:11,status:'queued',labels:['runnerhub-fast']},
    {id:4,runId:11,status:'waiting',labels:['runnerhub']},
    {id:5,runId:12,status:'pending',labels:['runnerhub']},
    {id:6,runId:12,status:'queued',labels:['ubuntu-latest']},
    {id:7,runId:12,status:'queued',labels:[]}
  ]};
  const summary=vm.runInContext('summarizeQueuedGitHubJobs(sampleQueue)',ctx);
  assert.equal(summary.queuedJobs,3);
  assert.equal(summary.queuedRuns,2);
  assert.equal(summary.waitingJobs,2);
  assert.equal(summary.otherJobs,2);
  assert.equal(summary.partial,true);
  assert.equal(summary.checkedRuns,3);
});

test('queue inspection only scans visible connected private repositories and marks failures unavailable',async()=>{
  const endpoints={
    '/api/v1/github/repositories/eutelic/runnerhub/queued-jobs':{
      repository:'eutelic/runnerhub',checkedRuns:1,truncated:true,jobs:[
        {id:10,runId:99,status:'queued',labels:['runnerhub']},
        {id:11,runId:99,status:'queued',labels:['runnerhub-publisher']},
        {id:12,runId:99,status:'queued',labels:['ubuntu-latest']}
      ]
    },
    '/api/v1/github/repositories/eutelic/second/queued-jobs':new Error('GitHub throttled')
  };
  const {ctx,element,calls}=createDashboard(endpoints);
  ctx.queueRepos=[
    {fullName:'eutelic/runnerhub',private:true},
    {fullName:'eutelic/second',private:true},
    {fullName:'eutelic/public',private:false}
  ];
  ctx.queuePools=[
    {scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'},githubInstallationId:42},
    {scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'},githubInstallationId:42},
    {scope:{kind:'repository',owner:'eutelic',repository:'second'},githubInstallationId:42},
    {scope:{kind:'repository',owner:'eutelic',repository:'public'},githubInstallationId:42},
    {scope:{kind:'repository',owner:'eutelic',repository:'not-connected'},githubInstallationId:0},
  ];
  vm.runInContext('auth={authenticated:true};snapshot.repos=queueRepos;snapshot.pools=queuePools;renderGitHubQueue()',ctx);
  assert.ok(element('github-queue-summary').textContent.includes('Not checked'));
  assert.equal(calls.filter(path=>path.includes('/queued-jobs')).length,0);
  await vm.runInContext('inspectGitHubQueues()',ctx);
  const checked=calls.filter(path=>path.endsWith('/queued-jobs'));
  assert.equal(checked.length,2);
  assert.ok(checked.some(path=>path.includes('/runnerhub/queued-jobs')));
  assert.ok(checked.some(path=>path.includes('/second/queued-jobs')));
  assert.ok(!checked.some(path=>path.includes('/public/')));
  const html=element('github-queue-summary').innerHTML;
  assert.ok(html.includes('2 observed queued RunnerHub jobs'));
  assert.ok(html.includes('1 inspection failure'));
  assert.ok(html.includes('≥ 2'));
  assert.ok(html.includes('Unavailable: GitHub throttled'));
  assert.ok(html.includes('Runs with queued RunnerHub jobs'));
  assert.ok(!element('github-queue-refresh').disabled);
});

test('no successful queue inspections never reports a false empty queue',async()=>{
  const {ctx,element}=createDashboard({
    '/api/v1/github/repositories/eutelic/runnerhub/queued-jobs':new Error('403 GitHub unavailable')
  });
  ctx.queueRepos=[{fullName:'eutelic/runnerhub',private:true}];
  ctx.queuePools=[{scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'},githubInstallationId:42}];
  vm.runInContext('auth={authenticated:true};snapshot.repos=queueRepos;snapshot.pools=queuePools',ctx);
  await vm.runInContext('inspectGitHubQueues()',ctx);
  const html=element('github-queue-summary').innerHTML;
  assert.ok(html.includes('queue depth unknown'));
  assert.ok(html.includes('Unavailable: 403 GitHub unavailable'));
  assert.ok(!html.includes('0 observed queued RunnerHub jobs'));
});

test('queue inspection pauses repeat Actions permission failures per installation without blocking others',async()=>{
  const permission=Object.assign(new Error('GitHub App cannot read Actions runs (403)'),{code:'github-actions-permission'});
  const endpoints={};
  const repoNames=['one','two','three','four','five'];
  for(const name of repoNames)endpoints['/api/v1/github/repositories/eutelic/'+name+'/queued-jobs']=permission;
  endpoints['/api/v1/github/repositories/example-user/personal/queued-jobs']={
    repository:'example-user/personal',checkedRuns:1,truncated:false,jobs:[{id:11,runId:12,status:'queued',labels:['runnerhub']}]
  };
  const {ctx,element,calls}=createDashboard(endpoints);
  ctx.queueRepos=[
    ...repoNames.map(name=>({fullName:'eutelic/'+name,private:true,installationId:42})),
    {fullName:'example-user/personal',private:true,installationId:88}
  ];
  ctx.queuePools=ctx.queueRepos.map(repo=>{
    const [owner,repository]=repo.fullName.split('/');
    return {scope:{kind:'repository',owner,repository},githubInstallationId:repo.installationId};
  });
  vm.runInContext('auth={authenticated:true};snapshot.repos=queueRepos;snapshot.pools=queuePools',ctx);
  await vm.runInContext('inspectGitHubQueues()',ctx);
  const orgCalls=calls.filter(path=>path.includes('/github/repositories/eutelic/')&&path.endsWith('/queued-jobs'));
  assert.ok(orgCalls.length>=2&&orgCalls.length<=3,'repeat 403s should not fan out to all 5 org repositories');
  assert.ok(calls.includes('/api/v1/github/repositories/example-user/personal/queued-jobs'),
    'separate installation remains inspectable');
  const html=element('github-queue-summary').innerHTML;
  assert.match(html,/GitHub App cannot read Actions runs/);
  assert.match(html,/Actions: read/);
  assert.match(html,/not checked after repeated authorization failures/);
  assert.match(html,/Not checked: Repeated Actions permission failures/);
  assert.match(html,/1 observed queued RunnerHub jobs/);
  assert.doesNotMatch(html,/5 inspection failure/);
});

test('queue inspection honors API-wide rate limit and does not claim an empty queue',async()=>{
  const limited=Object.assign(new Error('GitHub API rate limit reached'),{code:'github-rate-limited'});
  const endpoints={};
  const names=['one','two','three','four','five','six'];
  for(const name of names)endpoints['/api/v1/github/repositories/eutelic/'+name+'/queued-jobs']=limited;
  const {ctx,element,calls}=createDashboard(endpoints);
  ctx.queueRepos=names.map(name=>({fullName:'eutelic/'+name,private:true,installationId:42}));
  ctx.queuePools=names.map(name=>({scope:{kind:'repository',owner:'eutelic',repository:name},githubInstallationId:42}));
  vm.runInContext('auth={authenticated:true};snapshot.repos=queueRepos;snapshot.pools=queuePools',ctx);
  await vm.runInContext('inspectGitHubQueues()',ctx);
  assert.ok(calls.filter(path=>path.endsWith('/queued-jobs')).length<=2,'rate limit must stop new requests');
  const html=element('github-queue-summary').innerHTML;
  assert.match(html,/queue depth unknown/);
  assert.match(html,/rate limit reached/);
  assert.match(html,/Not checked/);
  assert.doesNotMatch(html,/0 observed queued RunnerHub jobs/);
});

test('fleet overview ignores merged archived enrollments in totals and offline warnings',()=>{
  const {ctx,element}=createDashboard();
  ctx.testHosts=[
    {id:'h1',name:'compute',state:'online'},
    {id:'h2',name:'truenas',state:'online'},
    {id:'h3',name:'compute-android',state:'online'},
    {id:'old-h1',name:'compute',state:'archived',mergedIntoHostId:'h1'},
    {id:'old-h2',name:'truenas',state:'archived',mergedIntoHostId:'h2'}
  ];
  vm.runInContext('renderFleetOverview(testHosts,[],[],[],[])',ctx);
  let html=element('fleet-overview').innerHTML;
  assert.match(html,/Hosts online[\s\S]*?3 \/ 3/);
  assert.doesNotMatch(html,/machine\(s\) not online/);
  assert.doesNotMatch(html,/No machines online/);

  ctx.testHosts[1].state='offline';
  vm.runInContext('renderFleetOverview(testHosts,[],[],[],[])',ctx);
  html=element('fleet-overview').innerHTML;
  assert.match(html,/Hosts online[\s\S]*?2 \/ 3/);
  assert.match(html,/1 machine\(s\) not online/);
  assert.doesNotMatch(html,/3 machine\(s\) not online/);

  vm.runInContext('renderFleetOverview(testHosts.slice(3),[],[],[],[])',ctx);
  html=element('fleet-overview').innerHTML;
  assert.match(html,/Hosts online[\s\S]*?0 \/ 0/);
  assert.match(html,/No active machines registered/);
  assert.doesNotMatch(html,/No machines online/);
});

test('publisher eligibility explains missing DinD host instead of reporting generic healthy fleet',()=>{
  const {ctx,element}=createDashboard();
  const pool={id:'publisher',scope:{owner:'eutelic',repository:'runnerhub'},
    githubInstallationId:42,scaleSetName:'runnerhub-publisher',maxConcurrency:2,
    isolation:'container-dind'};
  ctx.testPool=pool;
  vm.runInContext("renderFleetOverview([{id:'h1',name:'compute',state:'online',tags:{'isolation.container-trusted':'true'}}],[testPool],[],[{poolId:'publisher',state:'ready'}],[{poolId:'publisher',state:'healthy',healthy:true}])",ctx);
  const html=element('fleet-overview').innerHTML;
  assert.match(html,/No online host advertises isolated Docker-in-Docker support/);
  assert.match(html,/Review hosts/);
  const noMatch=vm.runInContext("publisherHostBlocker(testPool,[{id:'h1',state:'online',tags:{'isolation.container-dind':'true'}}])",ctx);
  assert.equal(noMatch,'');
});

test('publisher-specific required tags are diagnosed without assuming a GitHub queue',()=>{
  const {ctx,element}=createDashboard();
  ctx.testPool={id:'pub',scaleSetName:'runnerhub-publisher',maxConcurrency:2,
    scope:{owner:'acme',repository:'app'},hostSelector:{gpu:'true'}};
  const message=vm.runInContext("publisherHostBlocker(testPool,[{state:'online',tags:{'isolation.container-dind':'true'}}])",ctx);
  assert.match(message,/none match this pool's required host tags/);
  vm.runInContext("renderPools([testPool],[],[],new Map(),[{state:'online',tags:{'isolation.container-dind':'true'}}])",ctx);
  assert.match(element('pools').innerHTML,/Host diagnosis/);
  assert.match(element('pools').innerHTML,/none match this pool&#39;s required host tags|none match this pool's required host tags/);
});

test('finished GitHub job conflicting with busy RunnerHub worker has clear recovery guidance',()=>{
  const {ctx,element}=createDashboard();
  ctx.testPool={id:'pub',maxConcurrency:1,githubInstallationId:42,
    scaleSetName:'runnerhub-publisher',scope:{owner:'eutelic',repository:'runnerhub'}};
  ctx.testWorker={id:'worker-stale',poolId:'pub',state:'busy',jobName:'publish agent',
    repository:'eutelic/runnerhub',workflowRunId:41211256199,startedAt:'2026-10-31T17:40:09Z'};
  ctx.testProgress={workerId:'worker-stale',workflowRunId:41211256199,
    githubJobId:124571922351,available:true,status:'completed',conclusion:'failure',
    currentStep:'complete'};
  assert.equal(vm.runInContext('githubCompletedWorkerMismatch(testWorker,testProgress)',ctx),true);
  const link=vm.runInContext('githubRunLink(testWorker,testPool,testProgress)',ctx);
  assert.match(link,/actions\/runs\/41211256199\/job\/124571922351/);
  vm.runInContext("renderFleetOverview([{id:'h1',name:'compute',state:'online',tags:{'isolation.container-dind':'true'}}],[testPool],[testWorker],[{poolId:'pub',state:'ready'}],[{poolId:'pub',state:'active'}],[testProgress])",ctx);
  assert.match(element('fleet-overview').innerHTML,/GitHub\/RunnerHub state mismatch/);
  assert.match(element('fleet-overview').innerHTML,/GitHub \(failure\)/);
  vm.runInContext("renderActiveRunners([testWorker],new Map([['pub',testPool]]),new Map(),new Map([['worker-stale',testProgress]]))",ctx);
  assert.match(element('active-runners').innerHTML,/GitHub finished \(failure\); RunnerHub still busy/);
  assert.match(element('active-runners').innerHTML,/data-worker-reconcile="worker-stale"/);
  vm.runInContext("renderPools([testPool],[],[],new Map(),[{id:'h1',state:'online',tags:{'isolation.container-dind':'true'}}],[testWorker],[testProgress])",ctx);
  assert.match(element('pools').innerHTML,/finished on GitHub/);
  assert.equal(vm.runInContext("githubCompletedWorkerMismatch(testWorker,{available:true,status:'in_progress'})",ctx),false);
});

test('manual reconciliation requires terminal evidence and sends only the selected worker id',async()=>{
  const {ctx,element,calls}=createDashboard({
    '/api/v1/workers/worker-stale/reconcile-github':{
      detail:'GitHub completion confirmed and worker state reconciled.'
    }
  });
  ctx.testWorker={id:'worker-stale',poolId:'pub',state:'busy',jobName:'publish agent'};
  ctx.testProgress={workerId:'worker-stale',available:true,status:'completed',conclusion:'failure'};
  ctx.testButton={disabled:false,dataset:{workerReconcile:'worker-stale'}};
  vm.runInContext('snapshot.workers=[testWorker];snapshot.progress=[testProgress]',ctx);
  await vm.runInContext('reconcileFinishedGitHubWorker(testButton)',ctx);
  assert.ok(calls.includes('/api/v1/workers/worker-stale/reconcile-github'));
  assert.equal(ctx.testButton.disabled,false);
  assert.match(element('msg').textContent||element('msg').innerHTML,/reconciled/i);
});
test('legacy busy worker can supply a verified GitHub job URL without guessing the outcome',async()=>{
  const {ctx,element,calls,requestOptions}=createDashboard({
    '/api/v1/workers/old-worker/reconcile-github':{
      detail:'GitHub completion confirmed and worker state reconciled.'
    }
  });
  ctx.oldWorker={id:'old-worker',poolId:'pub',state:'busy',
    runnerName:'runnerhub-old',jobName:'publish (agent, runnerhub-agent)',
    startedAt:'2026-10-31T17:40:09Z',workflowRunId:0};
  ctx.oldPool={id:'pub',maxConcurrency:3,scope:{owner:'eutelic',repository:'runnerhub'}};
  vm.runInContext('snapshot.workers=[oldWorker];snapshot.progress=[]',ctx);
  vm.runInContext("renderActiveRunners([oldWorker],new Map([['pub',oldPool]]),new Map(),new Map())",ctx);
  assert.match(element('active-runners').innerHTML,/GitHub run ID missing/);
  assert.match(element('active-runners').innerHTML,/Verify completed GitHub job URL/);
  ctx.oldButton={dataset:{workerReconcile:'old-worker',workerNeedsUrl:'true'},disabled:false};
  await vm.runInContext('reconcileFinishedGitHubWorker(oldButton)',ctx);
  const endpoint='/api/v1/workers/old-worker/reconcile-github';
  assert.ok(calls.includes(endpoint));
  const posted=JSON.parse(requestOptions.get(endpoint)?.body||'{}');
  assert.equal(posted.githubJobUrl,
    'https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/124571922351');
  assert.equal(ctx.oldButton.disabled,false);
});
test('publisher at capacity identifies long-running worker without claiming a confirmed hang',()=>{
  const {ctx,element}=createDashboard();
  const started=new Date(Date.now()-2*24*60*60*1000).toISOString();
  ctx.testPool={id:'pub',maxConcurrency:1,scaleSetName:'runnerhub-publisher',
    githubInstallationId:42,scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'}};
  ctx.testBusy=[{id:'worker-old',poolId:'pub',state:'busy',jobName:'publish agent',
    startedAt:started,createdAt:started}];
  const diag=vm.runInContext('poolConcurrencyBlocker(testPool,testBusy)',ctx);
  assert.match(diag,/1\/1/);
  assert.match(diag,/Long-running publish agent/);
  assert.match(diag,/Verify its GitHub run/);
  assert.doesNotMatch(diag,/is hung|has stalled/);
  vm.runInContext("renderFleetOverview([{id:'h1',state:'online',tags:{'isolation.container-dind':'true'}}],[testPool],testBusy,[{poolId:'pub',state:'ready'}],[{poolId:'pub',state:'active'}])",ctx);
  assert.match(element('fleet-overview').innerHTML,/Long-running publish agent/);
  assert.match(element('fleet-overview').innerHTML,/Review runners/);
  vm.runInContext("renderPools([testPool],[],[],new Map(),[{id:'h1',state:'online',tags:{'isolation.container-dind':'true'}}],testBusy)",ctx);
  assert.match(element('pools').innerHTML,/Long-running publish agent/);
});

test('host edits survive subsequent renders and focused inputs are not replaced',()=>{
  const {ctx,element,document}=createDashboard();
  vm.runInContext(`renderHosts([{id:'host1',name:'box',state:'online',priority:2,userTags:{fast:'true'}}])`,ctx);
  assert.match(element('hosts').innerHTML,/value="fast"/);
  vm.runInContext(`hostDrafts.set('host1',{priority:'9',tags:'gpu, fast'})`,ctx);
  const focused = {tagName:'INPUT'};
  document.activeElement=focused;
  element('hosts').contains=el=>el===focused;
  const original=element('hosts').innerHTML;
  vm.runInContext(`renderHosts([{id:'host1',name:'box',state:'online',priority:2,userTags:{fast:'true'}}])`,ctx);
  assert.equal(element('hosts').innerHTML,original);
  document.activeElement={tagName:'BODY'};
  vm.runInContext(`renderHosts([{id:'host1',name:'box',state:'online',priority:2,userTags:{fast:'true'}}])`,ctx);
  assert.match(element('hosts').innerHTML,/value="gpu, fast"/);
  assert.match(element('hosts').innerHTML,/value="9"/);
  assert.match(element('hosts').innerHTML,/Save changes/);
});

test('hosts show online first, preserve separate IDs, and filter offline records',()=>{
  const {ctx,element}=createDashboard();
  const hosts=[
    {id:'old-identity',name:'compute',state:'offline',lastSeenAt:'2026-10-01T00:00:00Z',priority:1},
    {id:'live-identity',name:'compute',state:'online',lastSeenAt:'2026-11-02T15:00:00Z',priority:1},
    {id:'second-live',name:'compute',state:'online',lastSeenAt:'2026-11-02T14:00:00Z'}
  ];
  ctx.hostSamples=hosts;
  vm.runInContext('renderHosts(hostSamples)',ctx);
  const all=element('hosts').innerHTML;
  assert.ok(all.indexOf('data-host-id="live-identity"')<all.indexOf('data-host-id="old-identity"'));
  assert.match(all,/Other host IDs share this name/);
  assert.match(all,/3 active records/);
  element('host-filter').value='offline';
  vm.runInContext('renderHosts(hostSamples)',ctx);
  const filtered=element('hosts').innerHTML;
  assert.match(filtered,/data-host-id="old-identity"/);
  assert.doesNotMatch(filtered,/data-host-id="live-identity"/);
});

test('repair action prefills scheduling metadata but discards old trust and credentials',()=>{
  const {ctx,element}=createDashboard();
  ctx.hostSamples=[{id:'old-host',name:'compute',state:'offline',priority:4,userTags:{kvm:'true'}}];
  vm.runInContext('snapshot.hosts=hostSamples',ctx);
  element('machine-host-fingerprint').value='SHA256:old-key';
  element('machine-password').value='old-password';
  element('machine-allow-privileged').checked=false;
  vm.runInContext("recoverHost({dataset:{hostRecover:'old-host'}})",ctx);
  assert.equal(element('machine-name').value,'compute');
  assert.equal(element('machine-tags').value,'kvm');
  assert.equal(element('machine-priority').value,'4');
  assert.equal(element('machine-host-fingerprint').value,'');
  assert.equal(element('machine-password').value,'');
  assert.equal(element('machine-allow-privileged').checked,false);
  assert.match(element('machine-duplicate-warning').innerHTML,/Existing host name/);
});

test('bootstrap feedback remains visible and reports unknown outcome after reload',()=>{
  const session=new Map();
  const first=createDashboard({},new Map(),session);
  vm.runInContext("setMachineBootstrap('working',{name:'compute',startedAt:'2026-11-02T18:00:00Z',privateKey:'topsecret',password:'topsecret'})",first.ctx);
  assert.match(first.element('machine-bootstrap-status').innerHTML,/Enrollment in progress/);
  assert.doesNotMatch(session.get('runnerhub-machine-bootstrap-v1'),/password|privateKey|enrollmentToken|topsecret/);
  const second=createDashboard({},new Map(),session);
  assert.match(second.element('machine-bootstrap-status').innerHTML,/Enrollment status unconfirmed/);
  assert.match(second.element('machine-bootstrap-status').innerHTML,/inspect the host before retrying/i);
});
test('tracked bootstrap renders observed stages and persisted progress events',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext("machineOperation={id:'op-123',target:'compute',state:'waiting',step:'waiting-for-agent',detail:'Waiting for a new agent heartbeat.',lastProgressAt:'2026-11-02T19:00:00Z',events:[{state:'queued',step:'request-accepted',at:'2026-11-02T18:58:00Z'},{state:'working',step:'verifying-host-key',at:'2026-11-02T18:59:00Z'},{state:'waiting',step:'waiting-for-agent',at:'2026-11-02T19:00:00Z'}]};setMachineBootstrap('working',{operationId:'op-123',name:'compute',startedAt:'2026-11-02T18:58:00Z'})",ctx);
  const html=element('machine-bootstrap-status').innerHTML;
  assert.match(html,/Current stage:.*Waiting for agent heartbeat/);
  assert.match(html,/Verifying SSH host key/);
  assert.match(html,/Waiting for a new agent heartbeat/);
});

test('reload reconnects tracked operations without inventing a success outcome',async()=>{
  const session=new Map();
  session.set('runnerhub-machine-bootstrap-v1',JSON.stringify({
    state:'working',name:'compute',startedAt:'2026-11-02T18:00:00Z',operationId:'op-42'
  }));
  const op={id:'op-42',target:'compute',state:'waiting',step:'waiting-for-agent',
    detail:'Waiting for heartbeat.',startedAt:'2026-11-02T18:00:00Z',
    lastProgressAt:'2026-11-02T18:01:00Z',events:[]};
  const {ctx,element,calls}=createDashboard({'/api/v1/operations/op-42':op},new Map(),session);
  assert.match(element('machine-bootstrap-status').innerHTML,/Enrollment status unconfirmed/);
  await vm.runInContext('refreshMachineOperation()',ctx);
  assert.ok(calls.includes('/api/v1/operations/op-42'));
  assert.match(element('machine-bootstrap-status').innerHTML,/Enrollment in progress/);
  assert.match(element('machine-bootstrap-status').innerHTML,/Waiting for heartbeat/);
});
test('GitHub job links are only emitted for valid repository and run identifiers',()=>{
  const {ctx}=createDashboard();
  assert.match(vm.runInContext(`githubRunLink({repository:'acme/repo',workflowRunId:1234})`,ctx),/github.com\/acme\/repo\/actions\/runs\/1234/);
  assert.equal(vm.runInContext(`githubRunLink({repository:'<script>/repo',workflowRunId:1234})`,ctx),'—');
  assert.equal(vm.runInContext(`githubRunLink({repository:'acme/repo',workflowRunId:'abc'})`,ctx),'—');
});

test('partial endpoint failure does not block other dashboard data',async()=>{
  const endpoints={
    '/api/v1/hosts':[{id:'host1',name:'box',state:'online',priority:0,userTags:{}}],
    '/api/v1/pools':new Error('503 temporary failure'),
    '/api/v1/deployer/status':{available:false,detail:'Not installed'}
  };
  const {ctx,element,calls}=createDashboard(endpoints);
  await vm.runInContext('refresh()',ctx);
  assert.match(element('hosts').innerHTML,/box/);
  assert.match(element('data-freshness').textContent,/Partial update/);
  assert.match(element('fleet-data-warning').textContent,/pools/);
  assert.ok(calls.includes('/api/v1/hosts'));
  endpoints['/api/v1/pools']=[{id:'p1',scope:{kind:'repository',owner:'acme',repository:'repo'},maxConcurrency:2,githubInstallationId:12}];
  await vm.runInContext('refresh(true)',ctx);
  assert.match(element('data-freshness').textContent,/Updated/);
  assert.match(element('fleet-overview').innerHTML,/Listeners ready/);
});

test('history search pages results without requiring a full worker snapshot',async()=>{
  const url='/api/v1/workers/history?limit=20&offset=0&repository=&state=&search=';
  const endpoints={
    [url]:{items:[{id:'worker-1',repository:'eutelic/runnerhub',state:'failed',jobName:'Build',
      workflowRunId:123,updatedAt:'2026-10-30T12:00:00Z',failureReason:'disk full'}],
      total:21,hasMore:true}
  };
  const {ctx,element,calls}=createDashboard(endpoints);
  await vm.runInContext('loadHistory()',ctx);
  assert.ok(calls.includes(url));
  assert.match(element('history-results').innerHTML,/disk full/);
  assert.match(element('history-results').innerHTML,/actions\/runs\/123/);
  assert.match(element('history-count').textContent,/1–1 of 21/);
  assert.equal(element('history-next').disabled,false);
});
test('alert watch requires a sustained problem and excludes healthy idle pools',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext(`renderFleetAlerts(
    [{id:'h1',name:'compute',state:'online'}],
    [{id:'p1',scope:{owner:'eutelic',repository:'runnerhub'},githubInstallationId:1,maxConcurrency:1}],
    [],[{poolId:'p1',state:'ready'}],[{poolId:'p1',state:'healthy',healthy:true}]
  )`,ctx);
  assert.match(element('fleet-alerts').innerHTML,/No sustained problems detected/);
  vm.runInContext(`renderFleetAlerts(
    [{id:'h1',name:'compute',state:'offline'}],
    [],[],[],[]
  )`,ctx);
  assert.match(element('fleet-alerts').innerHTML,/being observed/);
  vm.runInContext(`fleetAlertFirstSeen.set('host:h1',Date.now()-6*60*1000)`,ctx);
  vm.runInContext(`renderFleetAlerts(
    [{id:'h1',name:'compute',state:'offline'}],[],[],[],[]
  )`,ctx);
  assert.match(element('fleet-alerts').innerHTML,/Host compute offline/);
  assert.match(element('fleet-alerts').innerHTML,/Mute 1h/);
});
test('onboarding never treats an unconnected listener as passing',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext(`auth.authenticated=true;renderOnboardingChecklist(
    [{id:'h1',state:'online'}],
    [{id:'p1',scope:{owner:'eutelic',repository:'runnerhub'},githubInstallationId:11,scaleSetName:'runnerhub'}],
    [{poolId:'p1',state:'backing_off'}],
    [{account:'eutelic'}],
    [{fullName:'eutelic/runnerhub',private:true}]
  )`,ctx);
  assert.match(element('repository-onboarding-checks').innerHTML,/5 \/ 6 preflight checks passed/);
  assert.match(element('repository-onboarding-checks').innerHTML,/0 of 1 ready/);
});

test('sustained fleet alert survives a page reload and resets after recovery',()=>{
  const offline=`renderFleetAlerts([{id:'h1',name:'compute',state:'offline'}],[],[],[],[])`;
  const first=createDashboard();
  vm.runInContext(offline,first.ctx);
  vm.runInContext(`fleetAlertFirstSeen.set('host:h1',Date.now()-6*60*1000);persistFleetAlertFirstSeen()`,first.ctx);
  assert.ok(first.storage.has('runnerhub-fleet-alert-first-seen-v1'));
  const second=createDashboard({},first.storage);
  vm.runInContext(offline,second.ctx);
  assert.match(second.element('fleet-alerts').innerHTML,/Host compute offline/);
  vm.runInContext(`renderFleetAlerts([{id:'h1',name:'compute',state:'online'}],[],[],[],[])`,second.ctx);
  assert.equal(JSON.parse(second.storage.get('runnerhub-fleet-alert-first-seen-v1')).length,0);
  vm.runInContext(offline,second.ctx);
  assert.match(second.element('fleet-alerts').innerHTML,/being observed/);
});
test('muted incidents remain visible and may be unmuted immediately',()=>{
  const {ctx,element,storage}=createDashboard();
  vm.runInContext(`fleetAlertFirstSeen.set('host:h1',Date.now()-6*60*1000)`,ctx);
  vm.runInContext(`fleetAlertSilences.set('host:h1',Date.now()+3600000)`,ctx);
  const offline=`renderFleetAlerts([{id:'h1',name:'compute',state:'offline'}],[],[],[],[])`;
  vm.runInContext(offline,ctx);
  assert.match(element('fleet-alerts').innerHTML,/Unmute/);
  assert.match(element('fleet-alerts').innerHTML,/Muted until/);
  element('fleet-alerts').handlers.click({
    target:{closest(){return {dataset:{fleetAlertSilence:'host:h1',fleetAlertAction:'unmute'}}}}
  });
  assert.equal(storage.get('runnerhub-fleet-alert-silences'),'[]');
  vm.runInContext(offline,ctx);
  assert.match(element('fleet-alerts').innerHTML,/Mute 1h/);
  assert.doesNotMatch(element('fleet-alerts').innerHTML,/Unmute/);
});
test('unchanged alert content does not overwrite a focused mute control',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext(`fleetAlertFirstSeen.set('host:h1',Date.now()-6*60*1000)`,ctx);
  const container=element('fleet-alerts');
  let writes=0,html='';
  Object.defineProperty(container,'innerHTML',{get(){return html},set(value){html=value;writes++}});
  const script=`renderFleetAlerts([{id:'h1',name:'compute',state:'offline'}],[],[],[],[])`;
  vm.runInContext(script,ctx);
  vm.runInContext(script,ctx);
  assert.equal(writes,1);
});

test('current runner telemetry ages out while retained peak metrics remain historical',()=>{
  const {ctx,element}=createDashboard();
  const now=Date.now();
  const recent=new Date(now-30000).toISOString();
  const old=new Date(now-10*60000).toISOString();
  assert.equal(vm.runInContext(`usageIsFresh({samples:1,updatedAt:'${recent}'},${now})`,ctx),true);
  assert.equal(vm.runInContext(`usageIsFresh({samples:1,updatedAt:'${old}'},${now})`,ctx),false);
  assert.equal(vm.runInContext("currentUsage({samples:0},'cpu')",ctx),'—');
  assert.equal(vm.runInContext(`currentUsage({samples:1,updatedAt:'${old}',currentCpuMillis:1200},'cpu')`,ctx),'stale');
  vm.runInContext(`renderActiveRunners([{
    id:'w1',poolId:'p1',hostId:'h1',state:'busy',repository:'eutelic/runnerhub',
    usage:{samples:5,updatedAt:'${old}',currentCpuMillis:1500,currentMemoryBytes:2147483648},
    resources:{cpuMillis:2000,memoryBytes:4294967296}
  }],new Map([['p1',{id:'p1',scaleSetName:'runnerhub'}]]),
    new Map([['h1',{id:'h1',name:'host'}]]),new Map())`,ctx);
  assert.match(element('active-runners').innerHTML,/stale/);
  assert.doesNotMatch(element('active-runners').innerHTML,/1.5 cores/);
});

test('history investigations restore shareable host, pool, UTC dates, and pagination',async()=>{
  const {ctx,element,calls,location,urlChanges,endpoints}=createDashboard();
  location.search='?h_repository=eutelic%2Frunnerhub&h_state=failed&h_search=timeout&h_host=host-22&h_pool=pool-7&h_from=2026-10-31&h_to=2026-11-02&h_offset=20&other=keep';
  location.hash='#activity';
  vm.runInContext('restoreHistoryFiltersFromURL()',ctx);
  assert.equal(element('history-repo').value,'eutelic/runnerhub');
  assert.equal(element('history-host').value,'host-22');
  assert.equal(element('history-pool').value,'pool-7');
  assert.equal(element('history-from').value,'2026-10-31');
  assert.equal(element('history-to').value,'2026-11-02');
  assert.equal(vm.runInContext('historyOffset',ctx),20);
  const url='/api/v1/workers/history?limit=20&offset=20&repository=eutelic%2Frunnerhub&state=failed&search=timeout&pool=pool-7&host=host-22&from=2026-10-31&to=2026-11-02';
  endpoints[url]={items:[],total:25,hasMore:false};
  await vm.runInContext('loadHistory()',ctx);
  assert.ok(calls.includes(url),JSON.stringify(calls));
  vm.runInContext('writeHistoryFiltersToURL()',ctx);
  assert.match(urlChanges.at(-1),/h_offset=20/);
  assert.match(urlChanges.at(-1),/h_pool=pool-7/);
  assert.match(urlChanges.at(-1),/other=keep/);
  assert.match(urlChanges.at(-1),/#activity$/);
});

test('history URL state rejects invalid page and preserves unrelated query parameters',()=>{
  const {ctx,element,location,urlChanges}=createDashboard();
  location.search='?h_offset=100001&unrelated=123';
  vm.runInContext('restoreHistoryFiltersFromURL()',ctx);
  assert.equal(vm.runInContext('historyOffset',ctx),0);
  element('history-repo').value='another/repo';
  element('history-from').value='2026-11-02';
  element('history-to').value='2026-11-01';
  assert.equal(vm.runInContext('historyDatesAreOrdered()',ctx),false);
  element('history-to').value='2026-11-02';
  vm.runInContext('writeHistoryFiltersToURL()',ctx);
  assert.match(urlChanges.at(-1),/h_repository=another%2Frepo/);
  assert.match(urlChanges.at(-1),/unrelated=123/);
  assert.doesNotMatch(urlChanges.at(-1),/h_offset=/);
});

test('history host/pool suggestions are grounded in known fleet records',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext(`snapshot.hosts=[{id:'host-7',name:'compute',state:'online'}];
    snapshot.pools=[{id:'pool-8',scaleSetName:'runnerhub-publisher',scope:{owner:'eutelic',repository:'runnerhub'}}];
    updateHistoryFilterSuggestions()`,ctx);
  assert.match(element('history-host-options').innerHTML,/host-7/);
  assert.match(element('history-host-options').innerHTML,/compute/);
  assert.match(element('history-pool-options').innerHTML,/pool-8/);
  assert.match(element('history-pool-options').innerHTML,/runnerhub-publisher/);
});

test('share link never leaks unrelated URL query parameters',async()=>{
  const {ctx,element,location}=createDashboard();
  location.search='?code=private-oauth-code&h_repository=old';
  element('history-repo').value='eutelic/runnerhub';
  let copied='';
  ctx.navigator={clipboard:{writeText:async text=>{copied=text}}};
  element('history-copy-link').handlers.click();
  // Clipboard promise resolves during microtask dispatch.
  await new Promise(resolve=>setImmediate(resolve));
  assert.match(copied,/h_repository=eutelic%2Frunnerhub/);
  assert.doesNotMatch(copied,/private-oauth-code|[?&]code=/);
  assert.match(copied,/#activity$/);
});

test('history GitHub links target the exact job when a validated job ID exists',()=>{
 const {ctx}=createDashboard();
 const job=vm.runInContext("githubRunLink({repository:'eutelic/runnerhub',workflowRunId:1234,jobId:'125574733491'})",ctx);
 assert.match(job,/actions\/runs\/1234\/job\/125574733491/);
 assert.match(job,/View job/);
 const fallback=vm.runInContext("githubRunLink({repository:'eutelic/runnerhub',workflowRunId:1234,jobId:'unexpected'})",ctx);
 assert.match(fallback,/actions\/runs\/1234"/);
 assert.doesNotMatch(fallback,/\/job\//);
});


test('clipboard failure never advises sharing an address bar with OAuth parameters',()=>{
  assert.match(source,/Clipboard unavailable\. No link was copied/);
  assert.match(source,/Do not share the address bar URL/);
  assert.doesNotMatch(source,/Copy the current address bar URL to share this investigation/);
});


test('Android emulator profile preset fills only trusted native KVM settings',()=>{
  const {ctx}=createDashboard();
  const html=vm.runInContext("runnerProfileEditor({fullName:'example-org/mobile-app',installationId:1},[],true)",ctx);
  assert.match(html,/data-android-emulator-preset/);
  assert.match(html,/Only the dedicated Android emulator profile/);

  const names=['name','tags','isolation','cpu','memory','concurrency','autotune'];
  const fields=new Map(names.map(name=>[name,{value:'',checked:true}]));
  const panel={querySelector(selector){
    const match=selector.match(/data-profile-field="([^"]+)"/);
    return match?fields.get(match[1]):null;
  }};
  ctx.testEmulatorPreset={closest(){return panel}};
  vm.runInContext("applyAndroidEmulatorPreset(testEmulatorPreset)",ctx);

  assert.equal(fields.get('name').value,'android-emulator');
  assert.equal(fields.get('tags').value,'android-emulator, hardware.kvm');
  assert.equal(fields.get('isolation').value,'host-persistent');
  assert.equal(fields.get('cpu').value,'4');
  assert.equal(fields.get('memory').value,'8');
  assert.equal(fields.get('concurrency').value,'1');
  assert.equal(fields.get('autotune').checked,false);
});

test('native KVM machine setup mode is separate from standard Docker bootstrap',()=>{
  const {ctx,element}=createDashboard();
  element('machine-mode').value='android-emulator';
  element('machine-name').value='compute';
  element('machine-tags').value='fast';
  vm.runInContext('chooseMachineMode()',ctx);
  assert.equal(element('machine-name').value,'compute-android');
  assert.match(element('machine-tags').value,/android-emulator/);
  assert.equal(element('machine-allow-privileged').checked,false);
  assert.equal(element('machine-allow-privileged').disabled,true);
  assert.match(element('add-machine').textContent,/KVM/);
  assert.match(element('machine-mode-help').textContent,/without touching its existing Docker agent/);
  element('machine-mode').value='docker';
  vm.runInContext('chooseMachineMode()',ctx);
  assert.equal(element('machine-allow-privileged').disabled,false);
});

test('online Docker hosts offer safe KVM onboarding without re-enrolling the Docker host',()=>{
  const {ctx,element}=createDashboard();
  vm.runInContext("renderHosts([{id:'h1',name:'compute',provider:'docker',state:'online',capacity:{cpuMillis:32000,memoryBytes:50000000000},userTags:{}}])",ctx);
  assert.match(element('hosts').innerHTML,/data-host-android="h1"/);
  assert.match(element('hosts').innerHTML,/Set up KVM/);
  vm.runInContext("renderHosts([{id:'h2',name:'compute-android',provider:'process',state:'online',userTags:{}}])",ctx);
  assert.doesNotMatch(element('hosts').innerHTML,/Set up KVM/);
});


test('archived enrollments are hidden by default and grouped under the live identity',()=>{
  const {ctx,element}=createDashboard();
  const hosts=[
    {id:'old-enrollment',name:'compute',state:'archived',provider:'docker',mergedIntoHostId:'current-enrollment',
      archivedAt:'2026-11-03T15:00:00Z'},
    {id:'current-enrollment',name:'compute',state:'online',provider:'docker',
      lastSeenAt:'2026-11-03T15:01:00Z'}
  ];
  ctx.hostSamples=hosts;
  vm.runInContext('renderHosts(hostSamples)',ctx);
  const html=element('hosts').innerHTML;
  assert.match(html,/data-host-id="current-enrollment"/);
  assert.doesNotMatch(html,/data-host-id="old-enrollment"/);
  assert.match(html,/Previous enrollments \(1\)/);
  assert.match(html,/old-enrollment/);
  assert.match(html,/1 archived/);
  assert.doesNotMatch(html,/Other host IDs share this name/);

  element('host-filter').value='archived';
  vm.runInContext('renderHosts(hostSamples)',ctx);
  const history=element('hosts').innerHTML;
  assert.match(history,/data-host-id="old-enrollment"/);
  assert.match(history,/Merged history into host ID/);
  assert.doesNotMatch(history,/data-host-recover=/);
});

test('offline duplicate offers archive and merge without auto-merging unrelated hosts',()=>{
  const {ctx,element}=createDashboard();
  ctx.hostSamples=[
    {id:'old',name:'compute',provider:'docker',state:'offline'},
    {id:'current',name:'compute',provider:'docker',state:'online'},
    {id:'different',name:'other',provider:'docker',state:'online'},
  ];
  vm.runInContext('renderHosts(hostSamples)',ctx);
  const html=element('hosts').innerHTML;
  assert.match(html,/data-host-archive="old"/);
  assert.match(html,/data-host-merge="true"/);
  assert.doesNotMatch(html,/data-host-archive="different"/);
});

test('merge requires typing the OLD host ID and sends only selected destination',async()=>{
  const {ctx,calls,requestOptions}=createDashboard();
  ctx.hostSamples=[
    {id:'old',name:'compute',provider:'docker',state:'offline'},
    {id:'current',name:'compute',provider:'docker',state:'online'}
  ];
  ctx.window.prompt=()=> 'old';
  vm.runInContext('snapshot.hosts=hostSamples',ctx);
  const button={dataset:{hostArchive:'old',hostMerge:'true'},disabled:false};
  ctx.archiveButton=button;
  await vm.runInContext('archiveOldHost(archiveButton)',ctx);
  assert.ok(calls.includes('/api/v1/hosts/old/archive'));
  const submitted=JSON.parse(requestOptions.get('/api/v1/hosts/old/archive').body);
  assert.equal(submitted.targetHostId,'current');
  assert.equal(button.disabled,false);
});


test('publisher editor shows persisted disk budget instead of forcing 40 GiB',()=>{
  const {ctx}=createDashboard();
  ctx.publisherPool={id:'publisher',scope:{owner:'eutelic',repository:'runnerhub'},
    githubInstallationId:42,scaleSetName:'runnerhub-publisher',maxConcurrency:3,
    resources:{cpuMillis:2500,memoryBytes:8*1073741824,diskBytes:24*1073741824}};
  const html=vm.runInContext(
    "publisherEditor({fullName:'eutelic/runnerhub',installationId:42},publisherPool,{state:'ready'},{state:'healthy'},true,true)",
    ctx);
  assert.match(html,/data-publisher-field="disk"[^>]*value="24"/);
  assert.match(html,/data-publisher-field="concurrency"[^>]*value="3"/);
  assert.match(html,/disk on one Docker-in-Docker capable host/);
  const defaultHTML=vm.runInContext(
    "publisherEditor({fullName:'eutelic/runnerhub',installationId:42},null,null,null,true,true)",
    ctx);
  assert.match(defaultHTML,/data-publisher-field="disk"[^>]*value="40"/);
});

test('publisher save submits requested diskBytes instead of hardcoding 40 GiB',async()=>{
  const {ctx,requestOptions}=createDashboard();
  const values={
    '[data-publisher-field="cpu"]':{value:'2.5'},
    '[data-publisher-field="memory"]':{value:'8'},
    '[data-publisher-field="disk"]':{value:'20'},
    '[data-publisher-field="concurrency"]':{value:'3'},
    '[data-publisher-field="autotune"]':{checked:false},
  };
  const panel={open:true,querySelector(selector){return values[selector]||null}};
  ctx.saveButton={
    dataset:{publisherAction:'update',installation:'42',repo:'eutelic/runnerhub'},
    disabled:false,closest(){return panel}
  };
  await vm.runInContext('savePublisher(saveButton)',ctx);
  const options=requestOptions.get('/api/v1/github/publisher');
  assert.equal(options?.method,'POST');
  const saved=JSON.parse(options.body);
  assert.equal(saved.maxConcurrency,3);
  assert.equal(saved.resources.cpuMillis,2500);
  assert.equal(saved.resources.memoryBytes,8*1073741824);
  assert.equal(saved.resources.diskBytes,20*1073741824);
});

test('invalid publisher disk cannot trigger a configuration write',async()=>{
  const {ctx,requestOptions}=createDashboard();
  const values={
    '[data-publisher-field="cpu"]':{value:'2.5'},
    '[data-publisher-field="memory"]':{value:'8'},
    '[data-publisher-field="disk"]':{value:'0'},
    '[data-publisher-field="concurrency"]':{value:'3'},
    '[data-publisher-field="autotune"]':{checked:false},
  };
  const panel={open:true,querySelector(selector){return values[selector]||null}};
  ctx.saveButton={
    dataset:{publisherAction:'update',installation:'42',repo:'eutelic/runnerhub'},
    disabled:false,closest(){return panel}
  };
  await vm.runInContext('savePublisher(saveButton)',ctx);
  assert.equal(requestOptions.has('/api/v1/github/publisher'),false);
  values['[data-publisher-field="disk"]'].value='not-a-number';
  await vm.runInContext('savePublisher(saveButton)',ctx);
  assert.equal(requestOptions.has('/api/v1/github/publisher'),false);
});

test('Docker hosts expose advertised free disk separately from CPU/RAM reservations',()=>{
  const {ctx,element}=createDashboard();
  const hosts=[
    {id:'compute-host',name:'compute',provider:'docker',state:'online',
      capacity:{cpuMillis:32000,memoryBytes:56*1073741824,diskBytes:28*1073741824}},
    {id:'unknown-disk',name:'truenas',provider:'docker',state:'offline',
      capacity:{cpuMillis:6000,memoryBytes:32*1073741824,diskBytes:0}},
  ];
  ctx.testHosts=hosts;
  vm.runInContext('renderHosts(testHosts)',ctx);
  const html=element('hosts').innerHTML;
  assert.match(html,/compute[\s\S]*Disk available: 28 GiB/);
  assert.match(html,/Disk available is filesystem free space at the agent state directory, not total disk capacity/);
  assert.match(html,/Publisher\/DinD runners also reserve their configured disk budget/);
  assert.match(html,/Disk available: unknown/);
});


test('scale-set installation blockers are shown in pool and fleet diagnostics',()=>{
  const {ctx,element}=createDashboard();
  ctx.testPool={id:'pool-missing-installation',maxConcurrency:1,githubInstallationId:0,scaleSetDisabled:false,
    scaleSetName:'runnerhub-publisher',scope:{owner:'eutelic',repository:'erd_tool'}};
  ctx.testStatus={poolId:'pool-missing-installation',state:'error',
    detail:'GitHub App installation mapping is missing; reconnect this repository to register its scale set'};
  vm.runInContext('renderPools([testPool],[testStatus],[],new Map(),[])',ctx);
  assert.match(element('pools').innerHTML,/GitHub App installation mapping is missing/);
  vm.runInContext('renderFleetOverview([], [testPool], [], [testStatus], [], [])',ctx);
  assert.match(element('fleet-overview').innerHTML,/GitHub App installation mapping is missing/);
});


test('CI efficiency distinguishes unavailable queue data from a measured zero',()=>{
  const {ctx,element}=createDashboard();
  ctx.ciItems=[{repository:'eutelic/runnerhub',data:{
    jobs:1,
    queue:{overall:{samples:0,p50Seconds:null,p95Seconds:null}},
    coverage:{queueSamples:0,validationIdentityJobs:0,validationPendingJobs:1,
      analysisWindowComplete:false,detail:'analysis pending'},
    analyzer:{state:'pending'},
    waste:{provablyAvoidablePercent:0,confirmedPercent:0,potentialSavingsPercent:0}
  }}];
  vm.runInContext('renderCIEfficiency(ciItems)',ctx);
  assert.match(element('ci-efficiency').innerHTML,/— · 0\/1/);
  assert.equal(vm.runInContext("evidenceAwareWastePercent(0,{analysisWindowComplete:false},{state:'pending'},1)",ctx),'—');

  ctx.ciItems[0].data.queue.overall={samples:1,p50Seconds:0,p95Seconds:0};
  ctx.ciItems[0].data.coverage.queueSamples=1;
  vm.runInContext('renderCIEfficiency(ciItems)',ctx);
  assert.match(element('ci-efficiency').innerHTML,/0\.00s · 1\/1/);
});


test('resource learning shows recent maximum CPU peak and stale sample counts',()=>{
  const {ctx}=createDashboard();
  const observed=vm.runInContext("observedText({resourceSizing:{ready:true,cpuPeakP95Millis:2253,cpuPeakMaxMillis:10823,memoryPeakMaxBytes:2*1073741824}})",ctx);
  assert.match(observed,/2\.25 cores p95 \/ 10\.82 max \/ 2 GiB/);
  const learning=vm.runInContext("learningText({resourceSizing:{jobs:5,confidence:'low',staleJobs:30}})",ctx);
  assert.match(learning,/30 stale ignored/);
});
