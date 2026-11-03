const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require('node:path').join(__dirname,'app.js'),'utf8');
assert.match(source,/initPageNav\(\);\s*renderMachineBootstrap\(\);\s*refresh\(\);\s*refreshMachineOperation\(\);/);
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
  const ctx = vm.createContext({
    document,window:{addEventListener(){},confirm(){return true},prompt(){return 'https://github.com/eutelic/runnerhub/actions/runs/41211256199/job/124571922351'}},location:{
      hostname:'localhost',origin:'http://localhost:8788',hash:''
    },history:{pushState(){}},sessionStorage:{
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
  return {ctx,elements,element,document,calls,requestOptions,endpoints,storage,session};
}

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
