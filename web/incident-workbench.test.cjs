const {test}=require('node:test');
const assert=require('node:assert/strict');
const {summarize,timeline,runURL}=require('./incident-workbench.js');
const base={
  id:'w1',poolId:'p1',hostId:'h1',repository:'eutelic/runnerhub',
  workflowRunId:41159246771,jobName:'Build',state:'failed',
  failureReason:'insufficient disk',jobResult:'failure',
  createdAt:'2026-10-30T12:00:00Z',
  queuedAt:'2026-10-30T11:59:00Z',
  startedAt:'2026-10-30T12:05:00Z',
  failedAt:'2026-10-30T12:06:00Z',
  updatedAt:'2026-10-30T12:06:00Z'
};
const pools=[{id:'p1',scope:{owner:'eutelic',repository:'runnerhub'},scaleSetName:'runnerhub'}];
const hosts=[{id:'h1',name:'compute',state:'offline'}];
const listeners=[{poolId:'p1',state:'backing_off',updatedAt:'2026-10-30T12:08:00Z'}];
const runtimes=[{poolId:'p1',state:'degraded',reasons:['no compatible host']}];
test('timeline orders independently-reported runner events',()=>{
  assert.deepEqual(timeline(base).map(x=>x.label),[
    'GitHub job queued (reported)','Runner requested','Job started','Runner failed'
  ]);
});
test('matches a run ID and reports evidence without inventing a GitHub queue',()=>{
  const reports=summarize([base],pools,hosts,listeners,runtimes,'41159246771');
  assert.equal(reports.length,1);
  assert.match(reports[0].githubURL,/actions\/runs\/41159246771/);
  assert.equal(reports[0].listenerState,'backing_off');
  assert.equal(reports[0].runtimeState,'degraded');
  assert.match(reports[0].evidence.join(' | '),/insufficient disk/);
  assert.match(reports[0].evidence.join(' | '),/no compatible host/);
});
test('without a worker there is no fabricated queued job result',()=>{
  assert.deepEqual(summarize([],pools,hosts,listeners,runtimes,'12345'),[]);
});
test('rejects invalid GitHub run URL identities',()=>{
  assert.equal(runURL('invalid/repo/path',1),'');
  assert.equal(runURL('ok/repo',0),'');
  assert.equal(runURL('<script>/x',10),'');
  assert.equal(runURL('ok/repo','invalid'),'');
});
test('matching by failure and repository remains safe',()=>{
  assert.equal(summarize([base],pools,hosts,listeners,runtimes,'DISK').length,1);
  assert.equal(summarize([base],pools,hosts,listeners,runtimes,'unrelated').length,0);
  assert.equal(summarize([base],pools,hosts,listeners,runtimes,'eutelic/runnerhub').length,1);
});
