const {test} = require('node:test');
const assert = require('node:assert/strict');
const {summarize} = require('./queue-diagnostics.js');

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
