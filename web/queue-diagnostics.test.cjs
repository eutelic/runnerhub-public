const {test} = require('node:test');
const assert = require('node:assert/strict');
const {summarize, auditRepositories, planOnboarding, diagnoseQueuedJob} = require('./queue-diagnostics.js');

function pool(overrides = {}) {
  return {id: 'p1', name: 'default', scope: {owner:'eutelic',repository:'runnerhub'},
          githubInstallationId: 123, maxConcurrency: 2, ...overrides};
}
function healthy(overrides = {}) {
  return {poolId:'p1',healthy:true,onlineHosts:1,activeWorkers:0,busyWorkers:0,
          reasons:[],...overrides};
}
function listener(state = 'ready') {
  return {poolId:'p1',state,detail:'',updatedAt:new Date().toISOString()};
}

test('empty pool list is handled without inventing queue state', () => {
  assert.deepEqual(summarize([],[],[]), []);
});
test('healthy listener and runtime do not produce a false queue warning', () => {
  const result = summarize([pool()],[listener()],[healthy()]);
  assert.equal(result.length, 1);
  assert.equal(result[0].severity, 'ready');
  assert.match(result[0].explanation, /no known blockers/);
});
test('missing GitHub App installation is actionable', () => {
  const result = summarize([pool({githubInstallationId:0})],[listener()],[healthy()]);
  assert.equal(result[0].severity,'attention');
  assert.match(result[0].explanation,/No GitHub App installation/);
});
test('listener backoff is surfaced without diagnosing a specific underlying cause', () => {
  const result = summarize([pool()],[listener('backing_off')],[healthy()]);
  assert.match(result[0].explanation,/backoff/);
  assert.match(result[0].explanation,/may be involved/);
});
test('404 stale-worker cause is visible without blaming hardware or GitHub rate limits', () => {
  const status = {...listener('backing_off'),
    detail: 'listener retry attempt=2 at 2026-11-04T23:22:00Z; last failure: handle started job: runnerhub /api/v1/scale-events returned 404: scale-set worker not found'};
  const report = summarize([pool()],[status],[healthy()]);
  assert.equal(report[0].severity, 'attention');
  assert.match(report[0].explanation, /unknown worker.*HTTP 404/);
  assert.doesNotMatch(report[0].explanation, /may be involved/);
  assert.equal(report[0].detail, status.detail);
});

test('listener diagnostics identify GitHub session conflicts separately', () => {
  const status = {...listener('backing_off'),
    detail: 'listener retry attempt=2 at 2026-11-04T23:22:00Z; last failure: 409 Conflict opening message session'};
  const report = summarize([pool()],[status],[healthy()]);
  assert.match(report[0].explanation, /message-session conflict/);
  assert.doesNotMatch(report[0].explanation, /unknown worker/);
});

test('host rejection reasons and occupied concurrency are visible', () => {
  const result = summarize([pool()],[listener()],[healthy({
    healthy:false,onlineHosts:0,activeWorkers:2,busyWorkers:2,
    reasons:['agent-1: insufficient RAM']})]);
  assert.match(result[0].explanation,/insufficient RAM/);
  assert.match(result[0].explanation,/additional jobs can wait/);
});
test('unknown listener is explicitly uncertain', () => {
  const result = summarize([pool()],[],[healthy()]);
  assert.equal(result[0].listener,'unknown');
  assert.match(result[0].explanation,/No listener status/);
});

test('repository audit shows disconnected private repositories and does not invent queued jobs', () => {
  const result = auditRepositories([
    {fullName:'eutelic/disconnected',private:true},
    {fullName:'eutelic/connected',private:true}
  ], [pool({scope:{kind:'repository',owner:'eutelic',repository:'connected'}})], [listener()], [healthy()]);
  assert.equal(result[0].name, 'eutelic/disconnected');
  assert.equal(result[0].code, 'no-standard-pool');
  assert.match(result[0].explanation, /Connect this private repository/);
  assert.equal(result[1].code, 'ready');
  assert.match(result[1].explanation, /no claim about GitHub job queue/);
});

test('public repositories cannot be mistaken for safe bulk-onboarding targets', () => {
  const result = auditRepositories([{fullName:'eutelic/public',private:false}],[],[],[]);
  assert.equal(result[0].code, 'public-unsupported');
});

test('publisher-only repo is not marked standard-connected', () => {
  const result = auditRepositories([{fullName:'eutelic/build',private:true}],
    [pool({scope:{kind:'repository',owner:'eutelic',repository:'build'},
      scaleSetName:'runnerhub-publisher',labels:['runnerhub-publisher']})],[],[]);
  assert.equal(result[0].code, 'no-standard-pool');
  assert.deepEqual(result[0].labels, ['runnerhub-publisher']);
});

test('listener backoff and zero eligible hosts receive different actionable findings', () => {
  const repos=[{fullName:'eutelic/runnerhub',private:true}];
  const pools=[pool({scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'},scaleSetName:'runnerhub'})];
  assert.equal(auditRepositories(repos,pools,[listener('backing_off')],[healthy()])[0].code,'listener-blocked');
  const result=auditRepositories(repos,pools,[listener()],[healthy({healthy:false,onlineHosts:0,reasons:['insufficient RAM']})]);
  assert.equal(result[0].code,'host-ineligible');
  assert.match(result[0].explanation,/insufficient RAM/);
});

test('repository audit is case-insensitive and never treats a different repository pool as connected', () => {
  const result=auditRepositories([
    {fullName:'EUTELIC/RUNNERHUB',private:true},{fullName:'eutelic/other',private:true}
  ],[pool({scope:{kind:'repository',owner:'eutelic',repository:'runnerhub'},scaleSetName:'runnerhub'})],
    [listener()],[healthy()]);
  assert.equal(result.find(r=>r.name==='EUTELIC/RUNNERHUB').code,'ready');
  assert.equal(result.find(r=>r.name==='eutelic/other').code,'no-standard-pool');
});

test('bulk plan only connects explicitly selected installed private repositories',()=>{
  const repos=[{fullName:'eutelic/a',private:true,installationId:42},{fullName:'eutelic/b',private:true,installationId:42},
    {fullName:'eutelic/public',private:false,installationId:42}];
  const plan=planOnboarding(['eutelic/a','eutelic/public','eutelic/a'],repos,[],[],{publisher:false,fast:false});
  assert.equal(plan.operations.length,1);
  assert.equal(plan.operations[0].kind,'standard');
  assert.equal(plan.operations[0].payload.repository,'eutelic/a');
  assert.equal(plan.operations[0].payload.installationId,42);
  assert.equal(plan.skipped.length,1);
});

test('bulk plan requires eligible hosts for optional isolated publisher and fast labels',()=>{
  const repos=[{fullName:'eutelic/a',private:true,installationId:42}];
  const plan=planOnboarding(['eutelic/a'],repos,[],[],{publisher:true,fast:true});
  assert.deepEqual(plan.operations.map(x=>x.kind),['standard']);
  assert.equal(plan.skipped.length,2);
  const withHosts=planOnboarding(['eutelic/a'],repos,[],[
    {state:'online',tags:{'isolation.container-dind':'true'}},
    {state:'online',tags:{fast:'true'}}
  ],{publisher:true,fast:true});
  assert.deepEqual(withHosts.operations.map(x=>x.kind),['standard','publisher','fast']);
  assert.equal(withHosts.operations[1].endpoint,'/api/v1/github/publisher');
  assert.equal(withHosts.operations[2].payload.name,'fast');
  assert.equal(withHosts.operations[2].payload.tags[0],'fast');
});

test('bulk plan is idempotent for already enabled standard publisher and fast pools',()=>{
  const repos=[{fullName:'eutelic/a',private:true,installationId:42}];
  const pools=['runnerhub','runnerhub-publisher','runnerhub-fast'].map((label,i)=>({
    id:String(i),scope:{kind:'repository',owner:'eutelic',repository:'a'},
    scaleSetName:label,githubInstallationId:42
  }));
  const result=planOnboarding(['eutelic/a'],repos,pools,[
    {state:'online',tags:{'isolation.container-dind':'true',fast:'true'}}
  ],{publisher:true,fast:true});
  assert.deepEqual(result.operations,[]);
});

test('pending job diagnostics distinguish no connection and unsupported label',()=>{
  const job={labels:['runnerhub-publisher']};
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[],[],[]).code,'not-connected');
  const pools=[{id:'p1',scope:{kind:'repository',owner:'eutelic',repository:'a'},
    scaleSetName:'runnerhub',githubInstallationId:42}];
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',pools,[],[]).code,'wrong-label');
});

test('pending job diagnostics distinguish JIT failures, placement and capacity',()=>{
  const p={id:'p1',scope:{kind:'repository',owner:'eutelic',repository:'a'},
    scaleSetName:'runnerhub',githubInstallationId:42,maxConcurrency:1};
  const job={labels:['runnerhub']};
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[p],
    [{poolId:'p1',state:'backing_off',detail:'JIT token 403'}],[]).code,'jit-failure');
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[p],
    [{poolId:'p1',state:'ready'}],[{poolId:'p1',healthy:false,onlineHosts:0}]).code,'no-compatible-host');
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[p],
    [{poolId:'p1',state:'ready'}],[{poolId:'p1',healthy:true,onlineHosts:1,activeWorkers:1}]).code,'capacity');
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[p],
    [{poolId:'p1',state:'ready'}],[{poolId:'p1',healthy:true,onlineHosts:1,activeWorkers:0}]).code,'unknown');
});

test('runtime history does not imply live host eligibility',()=>{
  const p={id:'p1',scope:{kind:'repository',owner:'eutelic',repository:'a'},scaleSetName:'runnerhub',githubInstallationId:42,maxConcurrency:2};
  const job={labels:['runnerhub']};
  const runtimeOnly={poolId:'p1',state:'healthy',activeWorkers:0,busyWorkers:0};
  assert.equal(diagnoseQueuedJob(job,'eutelic/a',[p],[listener()],[runtimeOnly]).code,'capacity-unverified');
  const valid={poolId:'p1',healthy:true,onlineHosts:1,activeWorkers:0,busyWorkers:0};
  const result=diagnoseQueuedJob(job,'eutelic/a',[p],[listener()],[valid]);
  assert.equal(result.code,'unknown');
  assert.match(result.detail,/compatible host/);
});

test('host resource exhaustion, offline hosts and pool limit are distinct',()=>{
  const p={id:'p1',scope:{kind:'repository',owner:'eutelic',repository:'a'},scaleSetName:'runnerhub',githubInstallationId:42,maxConcurrency:3};
  const job={labels:['runnerhub']};
  const get=health=>diagnoseQueuedJob(job,'eutelic/a',[p],[listener()],[health]).code;
  assert.equal(get({poolId:'p1',healthy:false,onlineHosts:0,activeWorkers:1,reasons:['compute: CPU exhausted']}),'host-capacity');
  assert.equal(get({poolId:'p1',healthy:false,onlineHosts:0,activeWorkers:0,reasons:['no recently online hosts']}),'no-compatible-host');
  assert.equal(get({poolId:'p1',healthy:false,onlineHosts:0,activeWorkers:3,reasons:['compute: CPU exhausted']}),'capacity');
});

test('missing GitHub job labels never imply the job requests another runner',()=>{
  const result=diagnoseQueuedJob({labels:[]},'eutelic/a',[],[],[]);
  assert.equal(result.code,'labels-unavailable');
});
