const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require('node:path').join(__dirname,'app.js'),'utf8');
assert.match(source,/initPageNav\(\);\s*refresh\(\);/);
const testSource = source.replace(/initPageNav\(\);\s*refresh\(\);\s*setInterval\([^\n]+\);/, 'initPageNav();');

function createDashboard(endpoints = {}, storage = new Map()) {
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
        querySelector(){return null}, querySelectorAll(){return []}, contains(){return false}
      });
    }
    return elements.get(id);
  };
  const calls = [];
  const document = {
    activeElement:{tagName:'BODY'},
    hidden:false,
    getElementById:element, querySelectorAll(){return []},
    body:{classList:{add(){}}}
  };
  const ctx = vm.createContext({
    document,window:{addEventListener(){}},location:{
      hostname:'localhost',origin:'http://localhost:8788',hash:''
    },history:{pushState(){}},sessionStorage:{
      getItem(key){return key==='runnerhub-token'?'test-token':null},setItem(){}
    },localStorage:{getItem(key){return storage.get(key)||null},setItem(key,value){storage.set(key,value)}},
    CSS:{escape(text){return text}},
    fetch:async path=>{
      calls.push(path);
      if(path==='/auth/github/status')return {json:async()=>({enabled:true,authenticated:true,csrf:'csrf',user:'tester'})};
      let value=endpoints[path]??[];
      if(value instanceof Error)throw value;
      return {ok:true,text:async()=>JSON.stringify(value)};
    },
    console,Date,Number,Map,Set,URLSearchParams
  });
  vm.runInContext(testSource,ctx,{filename:'app.js'});
  return {ctx,elements,element,document,calls,endpoints,storage};
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
