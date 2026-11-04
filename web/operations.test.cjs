const {test}=require('node:test');
const assert=require('node:assert/strict');
const {age,summarizeOperation}=require('./operations.js');

test('operation summary reports persisted stage and progress ages without inventing activity',()=>{
  const now=Date.parse('2026-11-04T12:00:00Z');
  const result=summarizeOperation({
    id:'op-1',target:'compute',state:'waiting',step:'waiting-for-agent',
    detail:'Waiting for first heartbeat.',startedAt:'2026-11-04T11:55:00Z',
    lastProgressAt:'2026-11-04T11:59:00Z',updatedAt:'2026-11-04T11:59:00Z',
    events:[{state:'working',step:'preparing-enrollment',at:'2026-11-04T11:55:00Z'}]
  },now);
  assert.equal(result.state,'waiting');
  assert.equal(result.step,'Waiting for agent heartbeat');
  assert.equal(result.detail,'Waiting for first heartbeat.');
  assert.equal(result.startedAge,'5m');
  assert.equal(result.lastProgressAge,'1m');
  assert.equal(result.events.length,1);
});

test('missing progress is reported as unknown rather than current activity',()=>{
  const result=summarizeOperation({state:'unknown',step:'server-restarted',lastProgressAt:''},Date.now());
  assert.equal(result.lastProgressAge,'unknown');
  assert.equal(result.step,'Control plane restarted');
  assert.equal(age('0001-01-01T00:00:00Z',Date.now()),'unknown');
});

test('unknown steps are humanized without changing their meaning',()=>{
  assert.equal(summarizeOperation({step:'native-kvm-unverified'}).step,'Native KVM unverified');
});
