const els={
  token:document.getElementById('apitoken'),
  hosts:document.getElementById('hosts'),
  pools:document.getElementById('pools'),
  activeRunners:document.getElementById('active-runners'),
  activeRunnerSummary:document.getElementById('active-runner-summary'),
  workers:document.getElementById('workers'),
  workerSummary:document.getElementById('worker-summary'),
  audit:document.getElementById('audit'),
  ciEfficiency:document.getElementById('ci-efficiency'),
  msg:document.getElementById('msg'),
  repos:document.getElementById('repos'),
  repoSearch:document.getElementById('repo-search'),
  repoShowPublic:document.getElementById('repo-show-public'),
  authstatus:document.getElementById('authstatus'),
  account:document.getElementById('account'),
  login:document.getElementById('login'),
  install:document.getElementById('install'),
  logout:document.getElementById('logout'),
  legacy:document.getElementById('legacy-auth'),
  deployerSummary:document.getElementById('deployer-summary'),
  deployerStatus:document.getElementById('deployer-status'),
  deployerAppsList:document.getElementById('deployer-apps-list'),
  deployerSetup:document.getElementById('deployer-setup'),
  deployerTokenResult:document.getElementById('deployer-token-result'),
  deployerWorkflowToken:document.getElementById('deployer-workflow-token'),
  fleetOverview:document.getElementById('fleet-overview'),
  fleetWarning:document.getElementById('fleet-data-warning'),
  dataFreshness:document.getElementById('data-freshness'),
  refreshNow:document.getElementById('refresh-now'),
  autoRefresh:document.getElementById('auto-refresh'),
  activeRunnerFilter:document.getElementById('active-runner-filter'),
  githubQueueRefresh:document.getElementById('github-queue-refresh'),
  githubQueueSummary:document.getElementById('github-queue-summary')
};
let csrf='';
let auth={enabled:false,authenticated:false};
let lastRepoRenderArgs=null;
let organizationPoolInstallations=[];
let organizationPoolRepositories=[];
let organizationPoolRecords=new Map();
let organizationPoolLoadedAccount='';
const bulkSelected=new Set();
let bulkConnecting=false;
let hiddenRepoAccounts=loadHiddenRepoAccounts();
const hostDrafts=new Map();
const hostUpdateChecks=new Map();
const hostUpdateOperations=new Map();
const snapshot={};
let githubQueueGeneration=0;
let githubQueueScan={loading:false,complete:false,at:0,rows:[],total:0};
const MACHINE_BOOTSTRAP_KEY='runnerhub-machine-bootstrap-v1';
let machineBootstrap=loadMachineBootstrap();
let machineOperation=null;
let machineOperationPolling=false;
let refreshInFlight=false;
let refreshPending=false;
let refreshPendingForce=false;
let historyOffset=0;
let historyRequestSequence=0;
let historyLastUpdated=0;
let repositoryInventoryAt=0;
let dashboardIdentity='';
let deployerAppsLastLoaded=0;
let deployerAppsBusy=false;
let deployerAppsError='';
function clearDashboardSnapshot(){
  deployerAppsLastLoaded=0;
  deployerAppsError='';
  if(els.deployerAppsList)els.deployerAppsList.textContent='Discovering TrueNAS apps…';
  for(const key of Object.keys(snapshot))delete snapshot[key];
  repositoryInventoryAt=0;
  githubQueueGeneration++;
  githubQueueScan={loading:false,complete:false,at:0,rows:[],total:0};
  renderGitHubQueue();
  recommendationCache={key:'',at:0,value:new Map(),errors:[]};
  efficiencyCache={key:'',at:0,value:[],errors:[]};
  hostDrafts.clear();
  hostUpdateChecks.clear();
  hostUpdateOperations.clear();
  historyLastUpdated=0;
  historyRequestSequence++;
}
const fleetAlertFirstSeen=loadFleetAlertFirstSeen();
const fleetAlertSilences=loadFleetAlertSilences();
const smokeWorkflow=[
  'name: RunnerHub smoke',
  'on:',
  '  workflow_dispatch:',
  'jobs:',
  '  smoke:',
  '    runs-on: runnerhub',
  '    timeout-minutes: 10',
  '    steps:',
  '      - name: Verify RunnerHub worker',
  '        shell: bash',
  '        run: |',
  '          set -euo pipefail',
  '          echo "Runner: $RUNNER_NAME"',
  '          echo "OS: $RUNNER_OS"',
  '          uname -a'
].join('\n');
let recommendationCache={key:'',at:0,value:new Map(),errors:[]};
let efficiencyCache={key:'',at:0,value:[],errors:[]};
const VERCEL_PROFILE_NAME='runnerhub-vercel';
const VERCEL_MACHINE_TAG='vercel';
const VERCEL_CLI_VERSION='59.16.0';
const vercelWorkflowTemplate=[
  'name: Deploy to Vercel',
  '',
  'on:',
  '  push:',
  '    branches: [main]',
  '  workflow_dispatch:',
  '',
  'concurrency:',
  '  group: vercel-production-${{ github.repository }}',
  '  cancel-in-progress: false',
  '',
  'jobs:',
  '  deploy:',
  '    runs-on: runnerhub-vercel',
  '    environment: production',
  '    permissions:',
  '      contents: read',
  '    env:',
  '      VERCEL_TOKEN: ${{ secrets.VERCEL_TOKEN }}',
  '      VERCEL_ORG_ID: ${{ vars.VERCEL_ORG_ID }}',
  '      VERCEL_PROJECT_ID: ${{ vars.VERCEL_PROJECT_ID }}',
  '    steps:',
  '      - uses: actions/checkout@v6',
  '      - uses: actions/setup-node@v6',
  '        with:',
  '          node-version: 24',
  '      - name: Pull Vercel production settings',
  '        run: npx --yes vercel@'+VERCEL_CLI_VERSION+' pull --yes --environment=production --token="'+ '$VERCEL_TOKEN' +'"',
  '      - name: Build Vercel output',
  '        run: npx --yes vercel@'+VERCEL_CLI_VERSION+' build --prod --token="'+ '$VERCEL_TOKEN' +'"',
  '      - name: Stage production deployment',
  '        id: deploy',
  '        shell: bash',
  '        run: |',
  '          set -euo pipefail',
  '          url="$(npx --yes vercel@'+VERCEL_CLI_VERSION+' deploy --prebuilt --prod --skip-domain --token="'+ '$VERCEL_TOKEN' +'")"',
  '          echo "url=$url" >> "$GITHUB_OUTPUT"',
  '          echo "### Vercel staged deployment" >> "$GITHUB_STEP_SUMMARY"',
  '          echo "$url" >> "$GITHUB_STEP_SUMMARY"',
  '      - name: Verify staged deployment',
  '        run: npx --yes vercel@'+VERCEL_CLI_VERSION+' inspect "${{ steps.deploy.outputs.url }}" --token="'+ '$VERCEL_TOKEN' +'"',
  '      - name: Promote to production',
  '        run: npx --yes vercel@'+VERCEL_CLI_VERSION+' promote "${{ steps.deploy.outputs.url }}" --yes --token="'+ '$VERCEL_TOKEN' +'"',
].join('\n');

els.token.value=sessionStorage.getItem('runnerhub-token')||'';
els.token.addEventListener('input',()=>{sessionStorage.setItem('runnerhub-token',els.token.value);refresh()});
bind('create','click',createPool);
bind('bulk-select-visible','click',()=>{
  els.repos.querySelectorAll('input[data-bulk-repo]:not(:disabled)').forEach(box=>bulkSelected.add(box.dataset.bulkRepo.toLowerCase()));
  rerenderRepositories();
});
bind('bulk-clear','click',()=>{bulkSelected.clear();rerenderRepositories()});
bind('bulk-connect','click',bulkConnectRepositories);
bind('machine-scan-fingerprint','click',scanMachineHostKey);
bind('machine-mode','change',chooseMachineMode); bind('add-machine','click',addMachine);
bind('machine-name','input',()=>renderMachineDuplicateWarning());
document.getElementById('machine-operations')?.addEventListener('click',event=>{
  const button=event.target.closest('button[data-operation-view]');
  if(!button)return;
  setMachineBootstrap('unknown',{operationId:button.dataset.operationView,
    name:button.dataset.operationName||'unknown',startedAt:new Date().toISOString()});
  refreshMachineOperation();
});
bind('host-filter','change',()=>{
  if(snapshot.hosts)renderHosts(snapshot.hosts,snapshot.workers||[],
    new Map((snapshot.pools||[]).map(p=>[p.id,p])));
});
document.getElementById('machine-bootstrap-status')?.addEventListener('click',event=>{
  if(!event.target.closest('[data-machine-bootstrap-dismiss]'))return;
  machineBootstrap=null;
  try{sessionStorage.removeItem(MACHINE_BOOTSTRAP_KEY)}catch{}
  renderMachineBootstrap();
});
bind('install-deployer','click',installDeployer);
bind('copy-deployer-token','click',copyDeployerToken);
bind('refresh-deployer-apps','click',()=>{void loadManagedDeployerApps(true)});
els.deployerAppsList?.addEventListener('change',event=>{
  const select=event.target.closest('select[data-deployer-repo]');
  if(!select)return;
  const imageSelect=select.closest('.deployer-auto-settings')?.querySelector('select[data-deployer-image]');
  if(!imageSelect)return;
  const match='ghcr.io/'+select.value.toLowerCase()+':latest';
  imageSelect.value=[...imageSelect.options].some(option=>option.value.toLowerCase()===match)?match:'';
});
els.deployerAppsList?.addEventListener('change',async event=>{
  const checkbox=event.target.closest('input[data-deployer-auto]');
  if(!checkbox)return;
  const app=checkbox.dataset.deployerAuto;
  const row=checkbox.closest('.deployer-app-row');
  const repository=row?.querySelector('[data-deployer-repo]')?.value||'';
  const workflow=row?.querySelector('[data-deployer-workflow]')?.value.trim()||'';
  const image=row?.querySelector('[data-deployer-image]')?.value||'';
  if(checkbox.checked&&(!repository||!workflow||!image)){
    checkbox.checked=false;
    message('Choose a GitHub publishing repository, workflow, and matching installed image for '+app+'.','error');
    return;
  }
  checkbox.disabled=true;
  try{
    await j('/api/v1/deployer/apps/'+encodeURIComponent(app)+'/automatic',{
      method:'POST',headers:{'content-type':'application/json'},
      body:JSON.stringify({enabled:checkbox.checked,repository,workflow,image})
    });
    message('Automatic publishing '+(checkbox.checked?'enabled':'disabled')+' for '+app+'.','success');
  }catch(e){
    message('Could not configure automatic publishing for '+app+': '+e.message,'error');
  }finally{
    await loadManagedDeployerApps(true);
  }
});
els.deployerAppsList?.addEventListener('change',async event=>{
  const checkbox=event.target.closest('input[data-deployer-enabled]');
  if(!checkbox)return;
  const app=checkbox.dataset.deployerEnabled;
  checkbox.disabled=true;
  try{
    await j('/api/v1/deployer/apps/'+encodeURIComponent(app)+'/enabled',{
      method:'POST',headers:{'content-type':'application/json'},
      body:JSON.stringify({enabled:checkbox.checked})
    });
    message(app+(checkbox.checked?' enabled for redeployment.':' disabled for redeployment.'),'success');
  }catch(e){
    message('Could not update '+app+': '+e.message,'error');
  }finally{
    await loadManagedDeployerApps(true);
  }
});
els.deployerAppsList?.addEventListener('click',async event=>{
  const button=event.target.closest('button[data-deployer-redeploy]');
  if(!button)return;
  const app=button.dataset.deployerRedeploy;
  if(!confirm('Pull updated images and redeploy '+app+' on TrueNAS now?'))return;
  button.disabled=true;
  button.textContent='Deploying…';
  try{
    await j('/api/v1/deployer/apps/'+encodeURIComponent(app)+'/redeploy',{method:'POST'});
    message(app+' redeployed successfully.','success');
  }catch(e){
    message('Could not redeploy '+app+': '+e.message,'error');
  }finally{
    await loadManagedDeployerApps(true);
  }
});
bind('message-dismiss','click',()=>message(''));
bind('github-queue-refresh','click',()=>{void inspectGitHubQueues()});
bind('history-filter-form','submit',event=>{
  event.preventDefault();
  historyOffset=0;
  if(!historyDatesAreOrdered())return;
  writeHistoryFiltersToURL();loadHistory();
});
bind('history-reset','click',()=>{
  for(const [,id] of historyQueryFields)document.getElementById(id).value='';
  historyOffset=0;writeHistoryFiltersToURL();loadHistory();
});
bind('history-prev','click',()=>{historyOffset=Math.max(0,historyOffset-20);writeHistoryFiltersToURL();loadHistory()});
bind('history-next','click',()=>{historyOffset+=20;writeHistoryFiltersToURL();loadHistory()});
bind('history-copy-link','click',async()=>{
  if(!historyDatesAreOrdered())return;
  writeHistoryFiltersToURL();
  // A share link intentionally contains ONLY history filters, never unrelated
  // URL parameters such as OAuth codes or third-party tracking credentials.
  const shared=new URLSearchParams();
  for(const [key,id] of historyQueryFields){
    const value=document.getElementById(id).value.trim();
    if(value)shared.set('h_'+key,value);
  }
  if(historyOffset)shared.set('h_offset',String(historyOffset));
  const link=location.origin+(location.pathname||'/')+(shared.toString()?'?'+shared:'')+'#activity';
  try{
    await navigator.clipboard.writeText(link);
    message('Investigation link copied. Opening it restores your filters and page.','success');
  }catch{
    message('Clipboard unavailable. No link was copied; enable clipboard access and retry. Do not share the address bar URL because it may contain unrelated sign-in parameters.','error');
  }
});
document.getElementById('smoke-workflow').value=smokeWorkflow;
document.getElementById('fleet-alerts')?.addEventListener('click',event=>{
  const button=event.target.closest('button[data-fleet-alert-silence]');
  if(!button)return;
  const key=button.dataset.fleetAlertSilence;
  if(button.dataset.fleetAlertAction==='unmute')fleetAlertSilences.delete(key);
  else fleetAlertSilences.set(key,Date.now()+60*60*1000);
  try{localStorage.setItem('runnerhub-fleet-alert-silences',JSON.stringify([...fleetAlertSilences]))}catch{}
  if(snapshot.hosts&&snapshot.pools&&snapshot.workers&&snapshot.scalesets&&snapshot.runtime)
    renderFleetAlerts(snapshot.hosts,snapshot.pools,snapshot.workers,snapshot.scalesets,snapshot.runtime);
});
bind('copy-smoke-workflow','click',async()=>{
  try{
    await navigator.clipboard.writeText(smokeWorkflow);
    message('Smoke workflow copied. Add it to a trusted private repo under .github/workflows/ and run it manually.','success');
  }catch{
    const textarea=document.getElementById('smoke-workflow');
    textarea.focus();textarea.select();
    message('Clipboard unavailable. The workflow is selected for manual copy.','error');
  }
});
const deployerEndpoint=document.getElementById('deployer-endpoint');
const deployerHealth=document.getElementById('deployer-health-url');
if(!deployerEndpoint.value&&location.hostname)deployerEndpoint.value='wss://'+location.hostname+'/api/current';
if(!deployerHealth.value)deployerHealth.value=location.origin.replace(/\/$/,'')+'/healthz';
els.logout.addEventListener('click',logout);
if(els.repoSearch)els.repoSearch.addEventListener('input',rerenderRepositories);
if(els.repoShowPublic){
  const saved=localStorage.getItem('runnerhub-show-public-unsupported');
  els.repoShowPublic.checked=saved===null?true:saved==='true';
  els.repoShowPublic.addEventListener('change',()=>{
    localStorage.setItem('runnerhub-show-public-unsupported',String(els.repoShowPublic.checked));
    rerenderRepositories();
  });
}
els.activeRunners.addEventListener('click',event=>{
  const button=event.target.closest('button[data-worker-reconcile]');
  if(button)reconcileFinishedGitHubWorker(button);
});
els.hosts.addEventListener('click',e=>{
  const scheduling=e.target.closest('button[data-host-scheduling-toggle]');
  if(scheduling){toggleHostScheduling(scheduling);return}
  const inspectUpdate=e.target.closest('button[data-host-update-status]');
  if(inspectUpdate){inspectHostAgentUpdateOperation(inspectUpdate);return}
  const startUpdate=e.target.closest('button[data-host-update-start]');
  if(startUpdate){startHostAgentUpdate(startUpdate);return}
  const update=e.target.closest('button[data-host-update-check]');
  if(update){checkHostAgentUpdate(update);return}
  const android=e.target.closest('button[data-host-android]');
  if(android){setupHostAndroid(android);return}
  const recover=e.target.closest('button[data-host-recover]');
  if(recover){recoverHost(recover);return}
  const archive=e.target.closest('button[data-host-archive]');
  if(archive){archiveOldHost(archive);return}
  const save=e.target.closest('button[data-host-tags]');
  if(save)saveHostTags(save);
});
els.hosts.addEventListener('input',e=>{
  const field=e.target.closest('[data-host-priority-input],[data-host-tag-input],[data-host-placement-domain-input]');
  if(!field)return;
  const id=field.dataset.hostPriorityInput||field.dataset.hostTagInput||field.dataset.hostPlacementDomainInput;
  const priority=els.hosts.querySelector('[data-host-priority-input="'+CSS.escape(id)+'"]');
  const tags=els.hosts.querySelector('[data-host-tag-input="'+CSS.escape(id)+'"]');
  const placementDomain=els.hosts.querySelector('[data-host-placement-domain-input="'+CSS.escape(id)+'"]');
  hostDrafts.set(id,{priority:priority?.value||'0',tags:tags?.value||'',placementDomain:placementDomain?.value||''});
  const save=els.hosts.querySelector('button[data-host-tags="'+CSS.escape(id)+'"]');
  if(save)save.textContent='Save changes';
});
els.activeRunnerFilter?.addEventListener('change',()=>{if(snapshot.workers)renderActiveRunners(snapshot.workers,
  new Map((snapshot.pools||[]).map(p=>[p.id,p])),new Map((snapshot.hosts||[]).map(h=>[h.id,h])),
  new Map((snapshot.progress||[]).map(p=>[p.workerId,p])));});
els.refreshNow?.addEventListener('click',()=>refresh(true));
els.repos.addEventListener('click',e=>{
  const learning=e.target.closest('button[data-learning-reset]');
  if(learning){resetPoolLearning(learning);return}
  const repoLearning=e.target.closest('button[data-repo-learning-reset]');
  if(repoLearning){resetRepositoryLearning(repoLearning);return}
  const vercelCopy=e.target.closest('button[data-vercel-copy]');
  if(vercelCopy){copyVercelWorkflow(vercelCopy);return}
  const vercel=e.target.closest('button[data-vercel-action]');
  if(vercel){saveVercelProfile(vercel);return}
  const emulatorPreset=e.target.closest('button[data-android-emulator-preset]');
  if(emulatorPreset){applyAndroidEmulatorPreset(emulatorPreset);return}
  const windowsPreset=e.target.closest('button[data-windows-native-preset]');
  if(windowsPreset){applyWindowsNativePreset(windowsPreset);return}
  const profile=e.target.closest('button[data-profile-action]');
  if(profile){saveRunnerProfile(profile);return}
  const publisher=e.target.closest('button[data-publisher-action]');
  if(publisher){savePublisher(publisher);return}
  const button=e.target.closest('button[data-resource-action]');
  if(button)saveRepositoryResources(button);
});
els.repos.addEventListener('change',e=>{
  const bulk=e.target.closest('input[data-bulk-repo]');
  if(bulk){
    const name=bulk.dataset.bulkRepo.toLowerCase();
    if(bulk.checked)bulkSelected.add(name);else bulkSelected.delete(name);
    updateBulkCount();
    return;
  }
  const account=e.target.closest('input[data-repo-account-toggle]');
  if(!account)return;
  setRepoAccountVisibility(account.dataset.repoAccountToggle,account.checked);
  rerenderRepositories();
});
document.getElementById('org-pool-account')?.addEventListener('change',()=>{
  organizationPoolLoadedAccount='';
  if(lastRepoRenderArgs)renderOrganizationPoolSetup(lastRepoRenderArgs[0],lastRepoRenderArgs[1],lastRepoRenderArgs[2],lastRepoRenderArgs[5]);
});
document.getElementById('org-pool-save')?.addEventListener('click',()=>saveOrganizationPool(true));
document.getElementById('org-pool-isolation')?.addEventListener('change',e=>{
  const name=document.getElementById('org-pool-name');
  if(name)name.value=e.target.value==='container-dind'?'runnerhub-publisher':'runnerhub-org';
});
document.getElementById('org-pool-existing')?.addEventListener('click',e=>{
  const button=e.target.closest('button[data-org-pool-toggle]');
  if(button)saveOrganizationPool(!organizationPoolRecords.get(button.dataset.orgPoolToggle)?.scaleSetDisabled,organizationPoolRecords.get(button.dataset.orgPoolToggle));
});

function loadHiddenRepoAccounts(){
  try{
    const value=JSON.parse(localStorage.getItem('runnerhub-hidden-repo-accounts')||'[]');
    return new Set((Array.isArray(value)?value:[]).map(x=>String(x).toLowerCase()).filter(Boolean));
  }catch{return new Set()}
}
function repoAccountVisible(account){return !hiddenRepoAccounts.has(String(account||'').toLowerCase())}
function setRepoAccountVisibility(account,visible){
  const key=String(account||'').toLowerCase();
  if(!key)return;
  if(visible)hiddenRepoAccounts.delete(key);else hiddenRepoAccounts.add(key);
  localStorage.setItem('runnerhub-hidden-repo-accounts',JSON.stringify([...hiddenRepoAccounts].sort()));
}
function rerenderRepositories(){if(lastRepoRenderArgs)renderRepos(...lastRepoRenderArgs)}
function updateBulkCount(){
  const count=document.getElementById('bulk-count');
  const button=document.getElementById('bulk-connect');
  if(count)count.textContent=bulkSelected.size+' selected';
  if(button)button.disabled=bulkConnecting||bulkSelected.size===0;
}
function esc(value){return String(value??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]))}
function show(el,on){el.classList.toggle('hidden',!on)}
function bind(id,event,handler){const el=document.getElementById(id);if(el)el.addEventListener(event,handler)}
function initPageNav(){
  const links=[...document.querySelectorAll('.page-nav a[data-page]')];
  const views=[...document.querySelectorAll('[data-page-view]')];
  if(!links.length||!views.length)return;
  const pageFromHash=()=>{
    const match=links.find(link=>link.getAttribute('href')===location.hash);
    return match?.dataset.page||links[0].dataset.page;
  };
  const activate=page=>{
    document.body.classList.add('page-navigation');
    views.forEach(view=>view.classList.toggle('page-view-active',view.dataset.pageView===page));
    links.forEach(link=>{
      if(link.dataset.page===page)link.setAttribute('aria-current','page');
      else link.removeAttribute('aria-current');
    });
  };
  links.forEach(link=>link.addEventListener('click',event=>{
    event.preventDefault();
    const href=link.getAttribute('href');
    if(location.hash!==href)history.pushState(null,'',href);
    activate(link.dataset.page);
    window.scrollTo(0,0);
    if(link.dataset.page==='activity'&&(auth.authenticated||els.token.value))loadHistory();
  }));
  window.addEventListener('popstate',()=>activate(pageFromHash()));
  window.addEventListener('hashchange',()=>activate(pageFromHash()));
  activate(pageFromHash());
}
function message(text,kind=''){
  els.msg.className='status-message'+(kind?' '+kind:'');
  els.msg.textContent=text||'';
  show(document.getElementById('message-container'),Boolean(text));
}
function gib(bytes){return bytes?Number((bytes/1073741824).toFixed(2)):0}
function cpuCores(millis){return millis?Number((millis/1000).toFixed(2)):0}
function usageCPU(millis){return Number.isFinite(Number(millis))&&Number(millis)>=0?cpuCores(Number(millis))+' cores':'—'}
function usageRAM(bytes){return Number.isFinite(Number(bytes))&&Number(bytes)>=0?gib(Number(bytes))+' GiB':'—'}
function usageIsFresh(usage,now=Date.now()){
  if(!Number(usage?.samples)||isZeroTime(usage?.updatedAt))return false;
  const stamp=Date.parse(usage.updatedAt);
  if(!Number.isFinite(stamp))return false;
  const age=now-stamp;
  // Do not label a last-known value "now" after an agent stalls. Allow
  // small clock skew and the normal interval between heartbeat samples.
  return age>=-30000&&age<=120000;
}
function currentUsage(usage,field){
  if(!Number(usage?.samples))return '—';
  if(!usageIsFresh(usage))return 'stale';
  return field==='cpu'?usageCPU(usage.currentCpuMillis):usageRAM(usage.currentMemoryBytes);
}
function sizingText(rec){const s=rec?.resourceSizing;if(!s?.ready)return s?.jobs?('learning ('+s.jobs+' jobs)'):'learning';return cpuCores(s.recommended?.cpuMillis)+' cores / '+gib(s.recommended?.memoryBytes)+' GiB'}
function observedText(rec){const s=rec?.resourceSizing;if(!s?.ready)return s?.jobs?('learning ('+s.jobs+' jobs)'):'—';const maxCpu=Number(s.cpuPeakMaxMillis||s.cpuPeakP95Millis);return cpuCores(s.cpuPeakP95Millis)+' cores p95 / '+cpuCores(maxCpu)+' max / '+gib(s.memoryPeakMaxBytes||s.memoryPeakP95Bytes)+' GiB'}
function learningText(rec){
  const s=rec?.resourceSizing;
  if(!s)return '—';
  let text=(s.jobs||0)+' valid job'+((s.jobs||0)===1?'':'s')+' · '+(s.confidence||'collecting');
  if(s.wellSampledJobs)text+=' · '+s.wellSampledJobs+' well-sampled';
  if(s.rejectedJobs)text+=' · '+s.rejectedJobs+' incompatible ignored';
  if(s.staleJobs)text+=' · '+s.staleJobs+' stale ignored';
  if(s.resetAt)text+=' · reset '+fmtTime(s.resetAt);
  return text;
}
function fmtDuration(value){return value&&value!=='0s'?value:'—'}
function bottleneckLabel(value){return ({pool_concurrency:'pool limit',host_resources:'host resources',no_compatible_host:'no compatible host'})[value]||value||'—'}
function reservedCPU(capacity){const used=cpuCores(capacity?.reserved?.cpuMillis);const total=cpuCores(capacity?.capacity?.cpuMillis);return total?used+' / '+total+' cores':'—'}
function reservedRAM(capacity){const used=gib(capacity?.reserved?.memoryBytes);const total=gib(capacity?.capacity?.memoryBytes);return total?used+' / '+total+' GiB':'—'}
function repoName(pool){return pool?.scope?.repository?pool.scope.owner+'/'+pool.scope.repository:pool?.scope?.owner||''}
function isPublisherPool(pool){return Boolean(pool)&&(String(pool.scaleSetName||'').toLowerCase()==='runnerhub-publisher'||(pool.labels||[]).some(x=>String(x).toLowerCase()==='runnerhub-publisher'))}
// Explain only eligibility blockers confirmed by the current host snapshot.
// Scheduling evidence from RunnerHub's own worker records. A long busy
// duration is a verification prompt, not proof that the worker is hung.
// This mismatch is confirmed by GitHub rather than inferred from elapsed time.
function githubCompletedWorkerMismatch(worker,progress){
  return String(worker?.state||'').toLowerCase()==='busy' &&
    progress?.available===true &&
    String(progress.status||'').toLowerCase()==='completed';
}
function legacyBusyWorkerNeedsJobURL(worker){
  if(String(worker?.state||'').toLowerCase()!=='busy'||Number(worker.workflowRunId)>0||
    !String(worker.runnerName||'').trim())return false;
  const at=Date.parse(worker.startedAt||'');
  return Number.isFinite(at)&&Date.now()-at>6*60*60*1000;
}
function staleGitHubWorkerDetail(worker,progress){
  if(!githubCompletedWorkerMismatch(worker,progress))return '';
  return (worker.jobName||'Job')+' has finished on GitHub ('+
    (progress.conclusion||'outcome unreported')+
    '), but RunnerHub still marks worker '+(worker.id||'unknown')+
    ' busy and reserves capacity. Inspect the worker and reconcile safely before retrying.';
}
function poolConcurrencyBlocker(pool,workers){
  const limit=Number(pool?.maxConcurrency||0);
  if(!Number.isFinite(limit)||limit<=0)return '';
  const active=(Array.isArray(workers)?workers:[]).filter(w=>
    w.poolId===pool.id&&!['deleted','completed','failed'].includes(String(w.state||'').toLowerCase()));
  if(active.length<limit)return '';
  const busy=active.filter(w=>String(w.state||'').toLowerCase()==='busy').sort((a,b)=>
    new Date(a.startedAt||a.createdAt||0)-new Date(b.startedAt||b.createdAt||0));
  const oldest=busy[0];
  let detail='Pool is at its configured runner limit ('+active.length+'/'+limit+').';
  if(oldest){
    const started=Date.parse(oldest.startedAt||oldest.createdAt||'');
    const age=Number.isFinite(started)?Math.max(0,Date.now()-started):0;
    if(age>6*60*60*1000){
      detail+=' Long-running '+(oldest.jobName||'job')+' on worker '+(oldest.id||'unknown')+
        ' has been busy '+fmtAge(oldest.startedAt||oldest.createdAt)+
        '. Verify its GitHub run and host diagnostics before manual cleanup.';
      if(legacyBusyWorkerNeedsJobURL(oldest))
        detail+=' The worker has no recorded GitHub run ID; an admin can supply a completed GitHub job URL from Active runners.';
    }else{
      detail+=' '+(oldest.jobName||'A job')+' is running; wait for capacity or inspect the GitHub run.';
    }
  }else{
    detail+=' Active workers may be provisioning or cleaning up. Inspect their state before retrying.';
  }
  return detail;
}
function publisherHostBlocker(pool,hosts){
  if(!isPublisherPool(pool))return '';
  const fleet=Array.isArray(hosts)?hosts:[];
  const qualified=fleet.filter(h=>String(h.state||'').toLowerCase()==='online'&&h.schedulingDisabled!==true&&
    String(h.tags?.['isolation.container-dind']||'').toLowerCase()==='true');
  if(!qualified.length){
    return 'No online host advertises isolated Docker-in-Docker support. Check Hosts and the machine publisher capability; do not enable privileged workers without verifying its trust level.';
  }
  const selector=pool?.hostSelector||{};
  const matching=qualified.filter(h=>Object.entries(selector).every(([key,value])=>
    String(h.tags?.[key]||'')===String(value)));
  if(!matching.length){
    return 'Online Docker-in-Docker hosts exist, but none match this pool\'s required host tags. Check the pool selector and Host tags.';
  }
  return '';
}
function isVercelProfile(pool){return Boolean(pool)&&String(pool.scaleSetName||'').toLowerCase()===VERCEL_PROFILE_NAME}
function isStandardPool(pool){const name=String(pool?.scaleSetName||'').toLowerCase();return Boolean(pool)&&(name===''||name==='runnerhub')}
function runnerClass(pool){if(!pool)return 'standard';if(isPublisherPool(pool))return 'publisher';if(isStandardPool(pool))return 'standard';return pool.scaleSetName||'custom'}
function resourceEnvelopeFactor(pool,worker){
  const isolation=String(worker?.isolation||pool?.isolation||'').toLowerCase();
  if(isolation!=='container-dind')return 1;
  return worker?.aggregateResources===true?1:2;
}
function aggregateBudgetEnabled(worker,pool){
  return String(worker?.isolation||pool?.isolation||'').toLowerCase()==='container-dind'&&worker?.aggregateResources===true;
}
function allocatedCPUText(worker,pool){
  const per=Number(worker?.resources?.cpuMillis||0);
  if(!per)return '—';
  const factor=resourceEnvelopeFactor(pool,worker);
  if(aggregateBudgetEnabled(worker,pool))return cpuCores(per)+' cores total budget';
  return factor===2?cpuCores(per)+' each · '+cpuCores(per*2)+' conservative reserve':cpuCores(per)+' cores';
}
function allocatedRAMText(worker,pool){
  const per=Number(worker?.resources?.memoryBytes||0);
  if(!per)return '—';
  const factor=resourceEnvelopeFactor(pool,worker);
  if(aggregateBudgetEnabled(worker,pool))return gib(per)+' GiB total budget';
  return factor===2?gib(per)+' GiB each · '+gib(per*2)+' conservative reserve':gib(per)+' GiB';
}
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
  if(!r.ok){
    const error=new Error(typeof v==='object'?(v.detail||v.error):v);
    if(v&&typeof v==='object'&&typeof v.code==='string')error.code=v.code;
    if(v&&typeof v==='object'&&typeof v.requestId==='string')error.requestId=v.requestId.slice(0,80);
    if(v&&typeof v==='object'&&typeof v.retryAt==='string')error.retryAt=v.retryAt;
    throw error;
  }
  return v;
}

function githubRunLink(worker,pool,progress){
  const name=worker.repository||repoName(pool);
  const id=Number(worker.workflowRunId);
  if(!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(String(name||''))||
      !Number.isSafeInteger(id)||id<=0)return '—';
  const [owner,repo]=name.split('/');
  const jobID=Number(progress?.githubJobId ?? worker.jobId);
  const specific=Number.isSafeInteger(jobID)&&jobID>0;
  return '<a href="https://github.com/'+encodeURIComponent(owner)+'/'+encodeURIComponent(repo)+
    '/actions/runs/'+id+(specific?'/job/'+jobID:'')+
    '" target="_blank" rel="noopener noreferrer">'+(specific?'View job':'View run')+'</a>';
}
function tab(rows,cols){
  if(!Array.isArray(rows)||!rows.length)return '<span class="muted">None</span>';
  return '<div class="table-wrap"><table><tr>'+cols.map(c=>'<th>'+esc(c[0])+'</th>').join('')+'</tr>'+
    rows.map(r=>'<tr>'+cols.map(c=>'<td>'+(c[2]===true?c[1](r):esc(c[1](r)))+'</td>').join('')+'</tr>').join('')+'</table></div>';
}

function renderPreservingTableScroll(container,html){
  if(!container)return;
  if(container.innerHTML===html)return;
  const scroll=[...container.querySelectorAll('.table-wrap')].map(w=>({left:w.scrollLeft,top:w.scrollTop}));
  const openDetails=[...container.querySelectorAll('details')].map(d=>d.open);
  container.innerHTML=html;
  container.querySelectorAll('details').forEach((d,i)=>{if(openDetails[i])d.open=true});
  container.querySelectorAll('.table-wrap').forEach((w,i)=>{
    const pos=scroll[i];
    if(!pos)return;
    w.scrollLeft=pos.left;
    w.scrollTop=pos.top;
  });
}

function hostTagText(host){return Object.keys(host?.userTags||{}).sort().join(', ')}
function hostDinDBudgetText(host){
  const tags=host?.tags||{};
  if(tags['feature.aggregateResourceGroups']==='true')return 'aggregate';
  if(tags['isolation.container-dind']==='true')return 'legacy x2';
  return '—';
}
function hostStateRank(host){
  return ({online:0,draining:1,offline:2,archived:3})[String(host?.state||'').toLowerCase()]??3;
}
function hostAgentUpdateBlockerLabel(code){
  const labels={
    'non-docker-agent':'This is not a Docker agent.',
    'host-not-online':'The host is not online.',
    'active-workers-or-cleanup':'Workers or cleanup reservations are still active.',
    'immutable-verified-target-required':'The control plane must select a verified immutable agent image (not :latest).',
    'verified-release-metadata-required':'Configure the verified image digest and exact release revision in RunnerHub before attempting an update.',
    'agent-updater-not-advertised':'This agent does not advertise the in-place updater. An older build may need a one-time upgrade.',
    'update-executor-not-implemented':'The detached updater, rollback and same-ID verification are not implemented yet.',
    'agent-update-feature-disabled':'One-click updates are disabled in the control-plane configuration.',
    'pinned-helper-image-unavailable':'The official detached updater helper image must be pinned to a published digest.',
    'agent-already-current':'This host already reports the configured target release.'
  };
  return labels[code]||'Update blocked: '+code;
}
async function checkHostAgentUpdate(button){
  const id=button.dataset.hostUpdateCheck;
  const host=(snapshot.hosts||[]).find(h=>h.id===id);
  if(!host||host.provider!=='docker')return message('Only Docker host agents can be inspected for image updates.','error');
  button.disabled=true;
  try{
    const report=await j('/api/v1/hosts/'+encodeURIComponent(id)+'/agent-update-preflight');
    if(report?.hostId!==id)throw new Error('The server returned a different host identity.');
    hostUpdateChecks.set(id,{report,at:new Date().toLocaleString()});
    renderHosts(snapshot.hosts||[],snapshot.workers||[],
      new Map((snapshot.pools||[]).map(p=>[p.id,p])));
    // One user action: run the server-side preflight and, only when
    // eligible, request confirmation for the durable update. A blocked
    // preflight is strictly read-only and shows the reason on the card.
    if(report.canUpdate){
      await startHostAgentUpdate({dataset:{hostUpdateStart:id},disabled:false});
    }
  }catch(e){message('Could not inspect '+host.name+' agent update: '+e.message,'error')}
  finally{button.disabled=false}
}

async function startHostAgentUpdate(button){
  const id=button.dataset.hostUpdateStart;
  const host=(snapshot.hosts||[]).find(h=>h.id===id);
  const report=hostUpdateChecks.get(id)?.report;
  if(!host||!report||!report.canUpdate)return message('Server preflight did not authorize this update.','error');
  if(!window.confirm('Update '+host.name+' ('+id+') to revision '+report.configuredTargetRevision+'? RunnerHub will drain this host, wait for running jobs and Docker cleanup, then replace the agent. Other hosts remain available.'))return;
  button.disabled=true;
  try{
    const result=await j('/api/v1/hosts/'+encodeURIComponent(id)+'/update-agent',{method:'POST'});
    if(result?.operation?.hostId!==id||!result?.operation?.operationId)throw new Error('The server did not confirm a durable update operation.');
    hostUpdateChecks.delete(id);
    hostUpdateOperations.set(id,result.operation);
    message('Agent update started for '+host.name+'. Operation '+result.operation.operationId+' · '+result.operation.state+'.','success');
    await refresh(true);
  }catch(e){message('Agent update could not start: '+e.message,'error')}
  finally{button.disabled=false}
}
async function inspectHostAgentUpdateOperation(button){
  const id=button.dataset.hostUpdateStatus;
  button.disabled=true;
  try{
    const op=await j('/api/v1/hosts/'+encodeURIComponent(id)+'/update-agent');
    if(op?.hostId!==id||!op?.operationId)throw new Error('Unexpected host update operation.');
    hostUpdateOperations.set(id,op);
    renderHosts(snapshot.hosts||[],snapshot.workers||[],
      new Map((snapshot.pools||[]).map(p=>[p.id,p])));
  }catch(e){message('Could not inspect host agent update: '+e.message,'error')}
  finally{button.disabled=false}
}
function renderHosts(hosts,workers=[],poolByID=new Map()){
  const hostsByID=new Map((hosts||[]).map(h=>[h.id,h]));
  const domainForHost=id=>{let current=id;const seen=new Set();while(current&&!seen.has(current)){seen.add(current);const host=hostsByID.get(current);if(!host)return 'host:'+current;if(String(host.state||'').toLowerCase()==='archived'&&host.mergedIntoHostId){current=host.mergedIntoHostId;continue}return host.placementDomainId?'domain:'+host.placementDomainId:'host:'+current}return 'host:'+id};
  const reservations=new Map();
  for(const w of workers||[]){
    if(['deleted','completed','failed'].includes(String(w.state||'').toLowerCase()))continue;
    if(!w.hostId)continue;
    const domain=domainForHost(w.hostId);
    const entry=reservations.get(domain)||{cpu:0,ram:0,active:0,busy:0};
    const factor=resourceEnvelopeFactor(poolByID.get(w.poolId),w);
    entry.cpu+=Number(w.resources?.cpuMillis||0)*factor;
    entry.ram+=Number(w.resources?.memoryBytes||0)*factor;
    entry.active++;
    if(String(w.state||'').toLowerCase()==='busy')entry.busy++;
    reservations.set(domain,entry);
  }
  renderMachineDuplicateWarning(hosts);
  // Leave edits and the caret alone during background refresh.
  if(els.hosts.contains(document.activeElement)&&
      ['INPUT','TEXTAREA','SELECT'].includes(document.activeElement.tagName))return;
  if(!Array.isArray(hosts)||!hosts.length){els.hosts.innerHTML='<span class="muted">No hosts enrolled.</span>';return}
  const previousOpen=new Set([...els.hosts.querySelectorAll('details[data-host-settings]')]
    .filter(el=>el.open).map(el=>el.dataset.hostSettings));
  const previousHistoryOpen=new Set([...els.hosts.querySelectorAll('details[data-host-previous]')]
    .filter(el=>el.open).map(el=>el.dataset.hostPrevious));
  const online=hosts.filter(h=>String(h.state||'').toLowerCase()==='online');
  const offline=hosts.filter(h=>String(h.state||'').toLowerCase()==='offline');
  const archived=hosts.filter(h=>String(h.state||'').toLowerCase()==='archived');
  const filter=document.getElementById('host-filter')?.value||'all';
  const visible=hosts.filter(h=>filter==='all'?String(h.state||'').toLowerCase()!=='archived':String(h.state||'').toLowerCase()===filter)
    .slice().sort((a,b)=>hostStateRank(a)-hostStateRank(b)||
      new Date(b.lastSeenAt||0)-new Date(a.lastSeenAt||0)||
      String(a.name||'').localeCompare(String(b.name||'')));
  const nameCounts=new Map();
  const sharedCapacityCandidates=new Set();
  for(const h of hosts){
    if(String(h.state||'').toLowerCase()==='archived')continue;
    const name=String(h.name||'').trim().toLowerCase();
    if(name)nameCounts.set(name,(nameCounts.get(name)||0)+1);
  }
  for(let i=0;i<hosts.length;i++)for(let j=i+1;j<hosts.length;j++){
    const a=hosts[i],b=hosts[j];
    if(String(a.state||'').toLowerCase()==='archived'||String(b.state||'').toLowerCase()==='archived'||String(a.provider||'').toLowerCase()===String(b.provider||'').toLowerCase())continue;
    const dimensions=['cpuMillis','memoryBytes','diskBytes','gpus'];
    if(dimensions.every(k=>Number(a.capacity?.[k]||0)===Number(b.capacity?.[k]||0))&&Number(a.capacity?.cpuMillis||0)>0&&Number(a.capacity?.memoryBytes||0)>0){sharedCapacityCandidates.add(a.id);sharedCapacityCandidates.add(b.id)}
  }
  const versions=[...new Set(online.map(h=>String(h.agentVersion||'').trim()).filter(Boolean))];
  const warnings=[];
  if(versions.length>1)warnings.push('<div class="resource-note warn"><strong>Different agent revisions on online hosts:</strong> '+
    online.map(h=>esc(h.name)+' ('+esc(String(h.agentVersion||'unknown').slice(0,8))+')').join(', ')+
    '. Revision hashes alone cannot establish which agent is newer; verify the intended deployment version before updating.</div>');
  const fallbackDinD=online.filter(h=>h.tags?.['isolation.container-dind']==='true'&&h.tags?.['feature.aggregateResourceGroups']!=='true');
  if(fallbackDinD.length)warnings.push('<div class="resource-note warn"><strong>DinD aggregate budget unavailable:</strong> '+
    fallbackDinD.map(h=>esc(h.name||h.id)).join(', ')+
    ' will keep conservative x2 CPU/RAM reservation until the aggregate cgroup probe passes.</div>');
  const disabledCount=hosts.filter(h=>String(h.state||'').toLowerCase()!=='archived'&&h.schedulingDisabled===true).length;
  const summary='<div class="host-counts">'+online.length+' online · '+offline.length+
    ' offline · '+archived.length+' archived · '+(hosts.length-archived.length)+' active records · '+disabledCount+' disabled for scheduling</div>';
  const previousByHost=new Map();
  const hostByID=new Map(hosts.map(h=>[h.id,h]));
  const domainCapacity=new Map();
  for(const h of hosts){if(String(h.state||'').toLowerCase()!=='online')continue;const key=domainForHost(h.id),old=domainCapacity.get(key);if(!old)domainCapacity.set(key,{...h.capacity});else for(const k of ['cpuMillis','memoryBytes','diskBytes','gpus']){if(Number(h.capacity?.[k]||0)<Number(old[k]||0))old[k]=Number(h.capacity?.[k]||0)}}
  const currentHostID=id=>{
    const visited=new Set();
    while(id&&!visited.has(id)){
      visited.add(id);
      const h=hostByID.get(id);
      if(!h)return '';
      if(String(h.state).toLowerCase()!=='archived')return id;
      id=h.mergedIntoHostId;
    }
    return '';
  };
  for(const retired of archived){
    const id=currentHostID(retired.mergedIntoHostId);
    if(!id)continue;
    const group=previousByHost.get(id)||[];
    group.push(retired);
    previousByHost.set(id,group);
  }
  const cards=visible.map(h=>{
    const state=String(h.state||'unknown').toLowerCase();
    const mergeTargets=hosts.filter(t=>String(t.state).toLowerCase()==='online'&&t.id!==h.id&&
      String(t.name||'').trim().toLowerCase()===String(h.name||'').trim().toLowerCase()&&t.provider===h.provider);
    const previous=previousByHost.get(h.id)||[];
    const previousNote=previous.length?'<details class="host-settings" data-host-previous="'+esc(h.id)+'"><summary>Previous enrollments ('+previous.length+')</summary>'+
      previous.map(old=>'<div class="host-meta">Archived '+esc(fmtTime(old.archivedAt))+
        ' · '+esc(old.name)+' · <code>'+esc(old.id)+'</code></div>').join('')+'</details>':'';
    const mergedNote=h.mergedIntoHostId?'<div class="host-identity-note">Merged history into host ID <code>'+
      esc(h.mergedIntoHostId)+'</code>. Historical workers retain this enrollment ID.</div>':'';
    const version=String(h.agentVersion||'').trim();
    const draft=hostDrafts.get(String(h.id));
    const domain=domainForHost(h.id);
    const capacity=domainCapacity.get(domain)||h.capacity||{};
    const used=reservations.get(domain)||{cpu:0,ram:0,active:0,busy:0};
    const sameName=nameCounts.get(String(h.name||'').trim().toLowerCase())>1;
    const sameNameNote=sameName
      ? '<div class="host-identity-note">Other host IDs share this name. They may be previous enrollments or separate machines; verify identity before cleanup.</div>'
      : '';
    const sharedCapacityNote=sharedCapacityCandidates.has(h.id)
      ? '<div class="host-identity-note">A different provider agent advertises the same CPU, RAM, disk, and GPU capacity. Verify whether it shares this physical machine, then set the same placement domain on both identities. Matching capacity alone does not merge or link hosts.</div>'
      : '';
    const offlineNote=state==='offline'
      ? '<div class="host-identity-note">Offline cause is not confirmed by heartbeat data. Check agent logs or use Repair / re-enroll.</div>'
      : '';
    const activeUpdate=hostUpdateOperations.get(h.id);
    const updateOperationNote=activeUpdate?'<div class="host-identity-note" role="status">'+
      '<strong>Agent update operation</strong> · '+esc(activeUpdate.state||'unknown')+
      ' · '+esc(activeUpdate.step||'')+
      '<div>Operation: <code>'+esc(activeUpdate.operationId||'')+'</code> · Target: <code>'+
      esc(String(activeUpdate.targetRevision||'').slice(0,8))+'</code></div>'+
      (['review','rolled-back'].includes(activeUpdate.state)?
       '<div>Host remains drained until the update outcome is reviewed.</div>':'')+
      '</div>':'';
    const updateCheck=hostUpdateChecks.get(h.id);
    const updateNote=updateCheck?'<div class="host-identity-note" role="status">'+
      '<strong>Agent update preflight</strong> · checked '+esc(updateCheck.at)+
      '<div>Installed revision: <code>'+esc(updateCheck.report.installedRevision||'unknown')+
      '</code> · Target revision: <code>'+esc(updateCheck.report.configuredTargetRevision||'not configured')+'</code> · Configured image: <code>'+esc(updateCheck.report.configuredTargetImage||'not configured')+'</code></div>'+
      '<div>Active worker reservations: '+esc(String(updateCheck.report.activeWorkers||0))+
      ' (cleanup: '+esc(String(updateCheck.report.cleanupReservations||0))+')</div>'+
      (updateCheck.report.canUpdate?'<div>Ready to start a graceful update. Existing jobs will finish before replacement.</div>':'<div>Update unavailable until:</div>')+'<ul>'+
      (updateCheck.report.blockers||[]).map(code=>'<li>'+esc(hostAgentUpdateBlockerLabel(code))+'</li>').join('')+
      '</ul>'+
      '</div>':'';
    const badgeClass=state==='online'?'badge':state==='offline'?'badge warn':'badge neutral';
    return '<article class="host-card" data-host-id="'+esc(h.id)+'">'+
      '<div class="host-card-top"><div class="host-heading"><strong>'+esc(h.name)+'</strong> '+
      '<span class="'+badgeClass+'">'+esc(h.state||'unknown')+'</span>'+ 
      (state==='archived'?'':' <span class="badge neutral">'+(h.schedulingDisabled?'Scheduling disabled':'Scheduling enabled')+'</span>')+'</div>'+
      (state==='online'&&String(h.provider||'').toLowerCase()==='docker'
       ? '<button type="button" class="secondary compact" data-host-update-check="'+esc(h.id)+'">Update agent</button>' : '')+
      (state!=='archived'&&String(h.provider||'').toLowerCase()==='docker'
       ? '<button type="button" class="secondary compact" data-host-update-status="'+esc(h.id)+'">Update status</button>' : '')+
      (state==='archived'?'':'<button type="button" class="secondary compact" data-host-scheduling-toggle="'+esc(h.id)+'" data-host-scheduling-disabled="'+(h.schedulingDisabled?'false':'true')+'">'+(h.schedulingDisabled?'Enable host':'Disable host')+'</button>')+
      (state==='archived'?'':'<button type="button" class="secondary compact" data-host-recover="'+esc(h.id)+'">Repair / re-enroll</button>')+
      (state==='offline'?'<button type="button" class="secondary compact" data-host-archive="'+esc(h.id)+'">Archive old host</button>':'')+
      (state==='offline'&&mergeTargets.length?'<button type="button" class="secondary compact" data-host-archive="'+esc(h.id)+'" data-host-merge="true">Merge into active host</button>':'')+
      (state==='online'&&String(h.provider||'').toLowerCase()==='docker'
       ? '<button type="button" class="secondary compact" data-host-android="'+esc(h.id)+'">Set up KVM</button>' : '')+'</div>'+
      '<div class="host-meta">Provider: '+esc(h.provider||'unknown')+' · Last seen: '+esc(!isZeroTime(h.lastSeenAt)?fmtAge(h.lastSeenAt)+' ago':'unknown')+
      ' · ID <code title="'+esc(h.id)+'">'+esc(h.id)+'</code></div>'+
      (h.placementDomainId?'<div class="host-identity-note">Shared physical placement domain <code>'+esc(h.placementDomainId)+'</code>; reservations shown here include every agent in that domain.</div>':'')+
      '<div class="host-meta">Workers: '+used.busy+' busy / '+used.active+' active · Agent revision: '+
      '<code title="'+esc(version)+'">'+esc(version?version.slice(0,8):'unknown')+'</code></div>'+
      (h.schedulingDisabled&&state!=='archived'?'<div class="host-identity-note">New runners will not be assigned here. Existing running workers are not stopped.</div>':'')+
      sameNameNote+sharedCapacityNote+offlineNote+mergedNote+previousNote+updateNote+updateOperationNote+
      (state==='archived'?'':'<details class="host-settings" data-host-settings="'+esc(h.id)+'"><summary>Capacity and scheduling settings</summary>'+
      '<div class="host-resources">'+(h.placementDomainId?'Shared placement-domain resources':'Agent capacity')+' · CPU: '+esc(capacity?.cpuMillis?cpuCores(used.cpu)+' / '+cpuCores(capacity.cpuMillis)+' cores':'unknown')+
      ' · RAM: '+esc(capacity?.memoryBytes?gib(used.ram)+' / '+gib(capacity.memoryBytes)+' GiB':'unknown')+
      ' · Disk available: '+esc(capacity?.diskBytes?gib(capacity.diskBytes)+' GiB':'unknown')+
      ' · DinD budget: '+esc(hostDinDBudgetText(h))+'</div>'+
      (String(h.provider||'').toLowerCase()==='docker'
        ? '<div class="resource-note muted">Disk available is filesystem free space at the agent state directory, not total disk capacity. Publisher/DinD runners also reserve their configured disk budget when scheduling.</div>'
        : '')+
      '<div class="resource-grid"><label>Priority<input class="inline-tags" data-host-priority-input="'+esc(h.id)+
      '" type="number" min="-1000" max="1000" step="1" value="'+esc(draft?.priority??Number(h.priority||0))+'"></label>'+
      '<label>Placement domain<input class="inline-tags" data-host-placement-domain-input="'+esc(h.id)+'" value="'+esc(draft?.placementDomain??h.placementDomainId??'')+'" placeholder="shared-machine-01"></label>'+
      '<div class="resource-note muted">Use the same operator-chosen ID only for agent identities on the same verified physical machine. Set RUNNERHUB_PLACEMENT_DOMAIN_ID on each agent to keep the domain through re-enrollment; domain IDs never match by hostname automatically.</div>'+
      '<label>Tags<input class="inline-tags" data-host-tag-input="'+esc(h.id)+'" value="'+
      esc(draft?.tags??hostTagText(h))+'" placeholder="gpu, high-memory"></label></div>'+
      '<button type="button" class="secondary compact" data-host-tags="'+esc(h.id)+'">'+
      (draft?'Save changes':'Save')+'</button></details>')+'</article>';
  }).join('');
  const html=summary+warnings.join('')+'<div class="host-list">'+(cards||'<div class="empty">No hosts match this filter.</div>')+'</div>';
  renderPreservingTableScroll(els.hosts,html);
  els.hosts.querySelectorAll('details[data-host-settings]').forEach(el=>{
    el.open=previousOpen.has(el.dataset.hostSettings);
  });
  els.hosts.querySelectorAll('details[data-host-previous]').forEach(el=>{
    el.open=previousHistoryOpen.has(el.dataset.hostPrevious);
  });
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
  const filter=els.activeRunnerFilter?.value||'all';
  const visible=active.filter(w=>{
    const state=String(w.state||'').toLowerCase();
    return filter==='all'||(filter==='busy'&&state==='busy')||
      (filter==='ready'&&state==='ready')||
      (filter==='starting'&&state!=='ready'&&state!=='busy');
  });
  const cpu=active.reduce((sum,w)=>{const pool=poolByID?.get(w.poolId);return sum+Number(w.resources?.cpuMillis||0)*resourceEnvelopeFactor(pool,w)},0);
  const memory=active.reduce((sum,w)=>{const pool=poolByID?.get(w.poolId);return sum+Number(w.resources?.memoryBytes||0)*resourceEnvelopeFactor(pool,w)},0);
  els.activeRunnerSummary.textContent=active.length+' active · '+cpuCores(cpu)+' cores · '+gib(memory)+' GiB host-reserved';
  if(!visible.length){
    els.activeRunners.innerHTML='<div class="empty">'+(active.length?'No active runners match this filter.':'No RunnerHub workers currently exist. GitHub may still have queued jobs: check the GitHub queue by repository below.')+'</div>';
    return;
  }

  renderPreservingTableScroll(els.activeRunners,tab(visible,[
    ['Repository',x=>x.repository||repoName(poolByID?.get(x.poolId))||'—'],
    ['Type',x=>runnerClass(poolByID?.get(x.poolId))],
    ['Job',x=>x.jobName||(String(x.state||'').toLowerCase()==='ready'?'idle / waiting':'startup')],
    ['GitHub',x=>githubRunLink(x,poolByID?.get(x.poolId),progressByWorker?.get(x.id)),true],
    ['Status',x=>{
      const p=progressByWorker?.get(x.id);
      if(githubCompletedWorkerMismatch(x,p)){
        return '<div class="runner-conflict"><strong>GitHub finished ('+esc(p.conclusion||'unknown')+
          '); RunnerHub still busy</strong><button type="button" class="secondary compact" '+
          'data-worker-reconcile="'+esc(x.id)+'">Reconcile / release slot</button></div>';
      }
      if(legacyBusyWorkerNeedsJobURL(x)){
        return '<div class="runner-conflict"><strong>GitHub run ID missing; unable to verify status automatically</strong>'+
          '<button type="button" class="secondary compact" data-worker-reconcile="'+esc(x.id)+
          '" data-worker-needs-url="true">Verify completed GitHub job URL</button></div>';
      }
      return esc(x.state||'unknown');
    },true],
    ['Steps completed',x=>{const p=progressByWorker?.get(x.id);return p?.available&&p.totalSteps>0?p.completedSteps+' / '+p.totalSteps+' ('+p.percent+'% steps)':'—'}],
    ['Current step',x=>progressByWorker?.get(x.id)?.currentStep||'—'],
    ['Host',x=>hostByID?.get(x.hostId)?.name||x.hostId||'pending'],
    ['CPU now · observed',x=>currentUsage(x.usage,'cpu')],
    ['RAM now · observed',x=>currentUsage(x.usage,'ram')],
    ['Last usage sample',x=>x.usage?.samples&&!isZeroTime(x.usage.updatedAt)?fmtTime(x.usage.updatedAt):'—'],
    ['CPU limit / reserve',x=>allocatedCPUText(x,poolByID?.get(x.poolId))],
    ['RAM limit / reserve',x=>allocatedRAMText(x,poolByID?.get(x.poolId))],
    ['Disk',x=>x.resources?.diskBytes?gib(x.resources.diskBytes)+' GiB':'—'],
    ['Runner started',x=>fmtTime(x.createdAt)],
    ['Job started',x=>fmtTime(x.startedAt)],
    ['Age',x=>fmtAge(x.createdAt)]
  ]));
}


function summarizeQueuedGitHubJobs(report){
  const jobs=Array.isArray(report?.jobs)?report.jobs:[];
  const runnerhub=jobs.filter(job=>Array.isArray(job.labels)&&
    job.labels.some(label=>/^runnerhub(?:-|$)/i.test(String(label))));
  const queued=runnerhub.filter(job=>String(job.status||'').toLowerCase()==='queued');
  const waiting=runnerhub.filter(job=>['waiting','pending'].includes(String(job.status||'').toLowerCase()));
  const other=jobs.length-runnerhub.length;
  const runs=new Set(queued.map(job=>String(job.runId||'')).filter(Boolean));
  return {queuedJobs:queued.length,queuedRuns:runs.size,waitingJobs:waiting.length,
    otherJobs:other,partial:report?.truncated===true,checkedRuns:Number(report?.checkedRuns)||0};
}

// Keep Github error classification in the server's sanitized response. Unknown
// errors remain per-repository rather than being guessed to affect every app.
function queueInspectionError(error){
  const code=String(error?.code||'');
  const labels={
    'github-actions-permission':'GitHub App Actions permission denied (403)',
    'github-rate-limited':'GitHub API rate limit reached',
    'github-auth-expired':'GitHub session expired or unauthorized',
    'github-repository-inaccessible':'Repository inaccessible to GitHub App (404)',
    'github-forbidden':'GitHub Actions access forbidden (403)',
    'github-api-failure':'GitHub Actions API request failed'
  };
  return {code,label:labels[code]||String(error?.message||error).slice(0,120)};
}

function eligibleQueueRepositories(){
  if(!auth.authenticated)return [];
  const visible=new Set((snapshot.repos||[]).filter(repo=>repo?.private===true)
    .map(repo=>String(repo.fullName||'').toLowerCase()));
  return [...new Set((snapshot.pools||[]).filter(pool=>
    Number(pool.githubInstallationId)>0&&pool.scope?.kind==='repository'&&
    /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repoName(pool))&&
    visible.has(repoName(pool).toLowerCase())).map(repoName))].sort((a,b)=>a.localeCompare(b));
}

function renderGitHubQueue(){
  const node=els.githubQueueSummary;
  if(!node)return;
  if(els.githubQueueRefresh)els.githubQueueRefresh.disabled=githubQueueScan.loading||!auth.authenticated;
  if(!auth.authenticated){
    node.textContent='Sign in with GitHub to inspect connected private repositories. A RunnerHub API token alone cannot query GitHub job queues.';
    return;
  }
  const scan=githubQueueScan;
  if(!scan.complete&&!scan.loading){
    node.textContent='Not checked. Click Check GitHub queues to inspect pending jobs. Zero active runners does not mean GitHub has no queued work.';
    return;
  }
  if(scan.complete&&scan.total===0){
    node.textContent='No connected private repositories available for GitHub queue inspection. Verify repository access and pool configuration.';
    return;
  }
  const rows=scan.rows.filter(Boolean);
  const failures=rows.filter(row=>row.error&&!row.skipped).length;
  const skipped=rows.filter(row=>row.skipped).length;
  const inspected=rows.filter(row=>!row.error&&!row.skipped);
  const queued=inspected.reduce((n,row)=>n+row.summary.queuedJobs,0);
  const waiting=inspected.reduce((n,row)=>n+row.summary.waitingJobs,0);
  const incomplete=inspected.filter(row=>row.summary.partial).length;
  const stamp=scan.at?new Date(scan.at).toLocaleTimeString([],{hour:'numeric',minute:'2-digit'}):'';
  const old=scan.at&&Date.now()-scan.at>2*60*1000;
  let html='<p class="muted small">'+(scan.loading?'Inspecting GitHub queues: '+rows.length+' / '+scan.total+' repositories…':
    'GitHub snapshot checked at '+esc(stamp)+(old?' (stale)':'')+'. ')+
    (scan.loading?'':inspected.length?
      esc(queued+' observed queued RunnerHub jobs across '+inspected.length+' repositories; '+
        waiting+' waiting/pending RunnerHub jobs.'):
      'No successful repository inspections; queue depth unknown.')+
    (failures?' '+failures+' inspection failure(s); counts do not include them.':'')+
    (skipped?' '+skipped+' not checked after repeated authorization failures or a rate limit.':'')+
    (incomplete?' '+incomplete+' repository result(s) truncated; counts are lower bounds.':'')+'</p>';
  if(rows.some(row=>row.errorCode==='github-actions-permission')){
    html+='<p class="muted small">GitHub App cannot read Actions runs. In GitHub App settings, verify <strong>Actions: read</strong>, approve pending installation permission changes, and sign out/in to refresh the GitHub session. <a href="https://github.com/settings/installations" target="_blank" rel="noopener noreferrer">Review GitHub App installations</a>. Jobs were not counted for inaccessible repositories.</p>';
  }else if(rows.some(row=>row.errorCode==='github-rate-limited')){
    const rate=rows.find(row=>row.errorCode==='github-rate-limited'&&row.retryAt);
    const reset=rate?new Date(rate.retryAt):null;
    const resetText=reset&&Number.isFinite(reset.getTime())?' GitHub retry time: '+esc(reset.toLocaleString())+'.':'';
    html+='<p class="muted small">GitHub API rate limit reached. Queue inspection was paused instead of making further requests. Retry after the limit resets.'+resetText+'</p>';
  }else if(rows.some(row=>row.errorCode==='github-auth-expired')){
    html+='<p class="muted small">The GitHub login cannot read Actions runs. Sign out and sign back in with GitHub, then retry.</p>';
  }
  if(rows.length){
    const sorted=[...rows].sort((a,b)=>
      (b.summary?.queuedJobs||0)-(a.summary?.queuedJobs||0)||
      a.repository.localeCompare(b.repository));
    html+=tab(sorted,[
      ['Repository',row=>{
        const parts=row.repository.split('/');
        return '<a href="https://github.com/'+parts.map(encodeURIComponent).join('/')+
          '/actions" target="_blank" rel="noopener noreferrer">'+esc(row.repository)+'</a>';
      },true],
      ['Queued RunnerHub jobs',row=>row.error?'Unavailable':(row.summary.partial?'≥ ':'')+row.summary.queuedJobs],
      ['Runs with queued RunnerHub jobs',row=>row.error?'Unavailable':(row.summary.partial?'≥ ':'')+row.summary.queuedRuns],
      ['Waiting / pending RunnerHub jobs',row=>row.error?'Unavailable':(row.summary.partial?'≥ ':'')+row.summary.waitingJobs],
      ['Other / unknown-label pending jobs',row=>row.error?'Unavailable':(row.summary.partial?'≥ ':'')+row.summary.otherJobs],
      ['Coverage',row=>row.skipped?'Not checked: '+row.error:row.error?
        'Unavailable: '+row.error+(row.requestId?' · GitHub request '+row.requestId:''):
        (row.summary.partial?'Partial':'Bounded')+' · '+row.summary.checkedRuns+' runs checked']
    ]);
  }
  html+='<p class="muted small">Only jobs observed in recent queued/in-progress GitHub workflow runs are counted. '+
    'Counts are point-in-time observations, not GitHub queue positions or scheduling guarantees. '+
    'Waiting/pending jobs may be blocked on approvals or workflow dependencies. '+
    'For a specific job\'s labels and blockers, use <a href="/diagnostics.html">Diagnostics</a>. '+
    'GitHub API inspection runs only when you click Check GitHub queues, not on the dashboard refresh timer.</p>';
  node.innerHTML=html;
}

async function inspectGitHubQueues(){
  if(githubQueueScan.loading||!auth.authenticated)return;
  const repositories=eligibleQueueRepositories();
  const generation=++githubQueueGeneration;
  githubQueueScan={loading:true,complete:false,at:0,rows:[],total:repositories.length};
  if(!repositories.length){
    githubQueueScan={loading:false,complete:true,at:Date.now(),
      rows:[],total:0};
    if(els.githubQueueSummary)els.githubQueueSummary.textContent=
      'No connected private repositories available for GitHub queue inspection. Verify repository access and pool configuration.';
    if(els.githubQueueRefresh)els.githubQueueRefresh.disabled=false;
    return;
  }
  renderGitHubQueue();
  let cursor=0;
  const responses=Array(repositories.length);
  const installationByRepo=new Map((snapshot.repos||[]).map(repo=>[
    String(repo.fullName||'').toLowerCase(),
    String(repo.installationId||String(repo.fullName||'').split('/')[0]).toLowerCase()
  ]));
  const permissionFailures=new Map();
  const pausedInstallations=new Set();
  let globalBlock=null;
  const inspect=async()=>{
    while(cursor<repositories.length&&generation===githubQueueGeneration){
      const index=cursor++;
      const repository=repositories[index];
      const installation=installationByRepo.get(repository.toLowerCase())||repository.split('/')[0].toLowerCase();
      if(globalBlock){
        responses[index]={repository,skipped:true,error:globalBlock.label,errorCode:globalBlock.code};
      }else if(pausedInstallations.has(installation)){
        responses[index]={repository,skipped:true,error:'Repeated Actions permission failures for this installation',errorCode:'github-actions-permission-paused'};
      }else{
        const path='/api/v1/github/repositories/'+repository.split('/').map(encodeURIComponent).join('/')+'/queued-jobs';
        try{
          const report=await j(path);
          responses[index]={repository,summary:summarizeQueuedGitHubJobs(report)};
        }catch(error){
          const issue=queueInspectionError(error);
          responses[index]={repository,error:issue.label,errorCode:issue.code,
            requestId:error?.requestId||'',retryAt:error?.retryAt||''};
          if(issue.code==='github-actions-permission'){
            const count=(permissionFailures.get(installation)||0)+1;
            permissionFailures.set(installation,count);
            // Do not fan out repeated App permission denials to every repo in
            // the same installation. One already in-flight request may finish.
            if(count>=2)pausedInstallations.add(installation);
          }else if(issue.code==='github-rate-limited'||issue.code==='github-auth-expired'){
            globalBlock=issue;
          }
        }
      }
      if(generation!==githubQueueGeneration)return;
      githubQueueScan.rows=responses.filter(Boolean);
      renderGitHubQueue();
    }
  };
  await Promise.all([inspect(),inspect()]);
  if(generation!==githubQueueGeneration)return;
  githubQueueScan={loading:false,complete:true,at:Date.now(),
    rows:responses.filter(Boolean),total:repositories.length};
  renderGitHubQueue();
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

  renderPreservingTableScroll(els.workers,tab(visible,[
    ['Repository',x=>x.repository||repoName(x)||'—'],
    ['Class',x=>runnerClass(poolByID?.get(x.poolId))],
    ['Job',x=>x.jobName||'startup'],
    ['GitHub',x=>githubRunLink(x,poolByID?.get(x.poolId)),true],
    ['Result',x=>workerDisplayState(x)],
    ['CPU now · total',x=>!terminal.has(String(x.state||'').toLowerCase())?currentUsage(x.usage,'cpu'):'—'],
    ['CPU peak · total',x=>x.usage?.samples?usageCPU(x.usage.peakCpuMillis):'—'],
    ['CPU peak · / container',x=>{
      const pool=poolByID?.get(x.poolId);
      const envelope=Number(x.usage?.peakEnvelopeCpuMillis||x.usage?.peakEnvelopeCPUMillis||0);
      return envelope?usageCPU(envelope):(resourceEnvelopeFactor(pool,x)===1&&x.usage?.samples?usageCPU(x.usage.peakCpuMillis):'—');
    }],
    ['RAM peak · total',x=>x.usage?.samples?usageRAM(x.usage.peakMemoryBytes):'—'],
    ['RAM peak · / container',x=>{
      const pool=poolByID?.get(x.poolId);
      const envelope=Number(x.usage?.peakEnvelopeMemoryBytes||0);
      return envelope?usageRAM(envelope):(resourceEnvelopeFactor(pool,x)===1&&x.usage?.samples?usageRAM(x.usage.peakMemoryBytes):'—');
    }],
    ['Limit / host reserve',x=>allocatedCPUText(x,poolByID?.get(x.poolId))+' / '+allocatedRAMText(x,poolByID?.get(x.poolId))],
    ['Updated',x=>fmtTime(workerTimestamp(x))],
    ['Failure',x=>x.failureReason||'']
  ])+'<div class="muted small workers-note">On aggregate-capable Docker hosts, publisher/DinD CPU and RAM are one total logical-worker budget shared by the runner, DinD daemon, and nested containers. Hosts that cannot prove aggregate cgroup enforcement keep the conservative two-envelope reservation and are excluded from DinD autosizing. Historical and deleted records remain available in Diagnostics.</div>');
}

function renderOnboardingChecklist(hosts,pools,statuses,installations,repos){
  const container=document.getElementById('repository-onboarding-checks');
  if(!container)return;
  const allHosts=(Array.isArray(hosts)?hosts:[]).filter(h=>String(h.state||'').toLowerCase()!=='archived');
  const allPools=Array.isArray(pools)?pools:[];
  const connected=allPools.filter(p=>Number(p.githubInstallationId)>0&&isStandardPool(p));
  const readyIDs=new Set((statuses||[]).filter(x=>x.state==='ready').map(x=>x.poolId));
  const online=allHosts.filter(h=>String(h.state||'').toLowerCase()==='online').length;
  const installationsCount=(installations||[]).length;
  const privateRepos=(repos||[]).filter(r=>r.private).length;
  const checks=[
    {ok:auth.authenticated,title:'Signed in with GitHub',help:auth.authenticated?'Authenticated session active':'Use Sign in with GitHub',href:'#repositories-section'},
    {ok:installationsCount>0,title:'GitHub App installed',help:installationsCount+' account installation(s)',href:'/auth/github/install'},
    {ok:online>0,title:'Compute host online',help:online+' of '+allHosts.length+' host(s)',href:'#compute-section'},
    {ok:privateRepos>0,title:'Private repository available',help:privateRepos+' accessible trusted private repo(s)',href:'#repositories-section'},
    {ok:connected.length>0,title:'Standard runner connected',help:connected.length+' connected runner pool(s)',href:'#repositories-section'},
    {ok:connected.some(pool=>readyIDs.has(pool.id)),title:'Scale-set listener ready',help:connected.filter(pool=>readyIDs.has(pool.id)).length+' of '+connected.length+' ready',href:'/diagnostics.html'}
  ];
  const count=checks.filter(item=>item.ok).length;
  container.innerHTML='<div class="quickstart-summary">'+count+' / '+checks.length+' preflight checks passed</div>'+
    '<div class="quickstart-grid">'+checks.map(item=>
      '<a class="quickstart-item" href="'+item.href+'"><span class="'+(item.ok?'state-good':'state-warn')+'">'+
      (item.ok?'✓':'!')+'</span><span><strong>'+esc(item.title)+'</strong>'+
      '<span class="muted small">'+esc(item.help)+'</span></span></a>'
    ).join('')+'</div>';
}


function guessedAutoPublishRepo(app){
  const repos=(snapshot.repos||[]).filter(repo=>repo?.private===true&&
    /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(String(repo.fullName||'')));
  // Prefer concrete image ownership over a TrueNAS app's display name.
  // Never guess when multiple connected repositories match the same app.
  const images=new Set((app.images||[]).map(image=>String(image).toLowerCase()));
  const imageMatches=repos.filter(repo=>images.has('ghcr.io/'+repo.fullName.toLowerCase()+':latest'));
  if(imageMatches.length===1)return imageMatches[0].fullName;
  if(imageMatches.length>1)return '';
  const normalized=str=>String(str||'').toLowerCase().replace(/[-_.]/g,'');
  const nameMatches=repos.filter(repo=>normalized(repo.fullName.split('/')[1])===normalized(app.name));
  return nameMatches.length===1?nameMatches[0].fullName:'';
}

function renderManagedDeployerApps(apps){
  if(!els.deployerAppsList)return;
  if(!apps.length){
    els.deployerAppsList.innerHTML='<div class="muted">No TrueNAS apps were discovered.</div>';
    return;
  }
  const repositories=(snapshot.repos||[]).filter(repo=>repo?.private===true&&
    /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(String(repo.fullName||'')));
  els.deployerAppsList.innerHTML=apps.map(app=>{
    const name=esc(app.name);
    const isProtected=Boolean(app.protected);
    const isCustomApp=Boolean(app.customApp);
    const enabled=Boolean(app.enabled);
    const automatic=app.automatic||null;
    const repoSelected=automatic?.repository||guessedAutoPublishRepo(app);
    const workflow=automatic?.workflow||'publish-images.yml';
    const images=(app.images||[]).filter(image=>/^ghcr\.io\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+:latest$/i.test(String(image||'')));
    const selectedImage=automatic?.image||images.find(image=>image.toLowerCase()==='ghcr.io/'+repoSelected.toLowerCase()+':latest')||'';
    const imageChoices=['<option value="">Choose installed image</option>'].concat(
      images.map(image=>'<option value="'+esc(image)+'" '+
        (image.toLowerCase()===selectedImage.toLowerCase()?'selected':'')+'>'+esc(image)+'</option>')
    ).join('');
    const repoChoices=['<option value="">Choose publishing repository</option>'].concat(
      repositories.map(repo=>'<option value="'+esc(repo.fullName)+'" '+
        (repo.fullName.toLowerCase()===repoSelected.toLowerCase()?'selected':'')+'>'+
        esc(repo.fullName)+'</option>')
    ).join('');
    const lastStatus=automatic?.lastResult?
      '<span class="muted small">Last publishing run: '+esc(automatic.lastResult)+
      (automatic.lastDetail?' · '+esc(automatic.lastDetail):'')+'</span>':'';
    const lastDeploy=app.lastDeployment;
    const deployStatus=lastDeploy?.result?
      '<span class="muted small">Last deployment: '+esc(lastDeploy.result)+
      (lastDeploy.completedAt?' · '+esc(fmtTime(lastDeploy.completedAt)):'')+
      (lastDeploy.detail?' · '+esc(lastDeploy.detail):'')+'</span>':'';
    return '<div class="deployer-app-row">'+
      '<div class="deployer-app-name"><strong>'+name+'</strong>'+
        '<span class="muted small">'+esc(app.state||'Unknown state')+
        (isProtected?' · Managed by existing deployment configuration':'')+'</span>'+
        lastStatus+deployStatus+
      '</div>'+
      '<div class="deployer-app-actions">'+
        '<label class="check-row"><input type="checkbox" data-deployer-enabled="'+name+'" '+
          (enabled?'checked ':'')+(isProtected?'disabled ':'')+'aria-label="Enable redeploy for '+name+'">'+
          ' Enable redeploy</label>'+
        '<button type="button" class="secondary compact" data-deployer-redeploy="'+name+'" '+
          ((!enabled||isProtected)?'disabled':'')+'>Redeploy now</button>'+
      '</div>'+
      ((!enabled||isProtected)?'':
        '<div class="deployer-auto-settings">'+
          '<label class="check-row"><input type="checkbox" data-deployer-auto="'+name+'" '+
            (automatic?'checked':'')+((isCustomApp||automatic)?'':' disabled')+'> Auto-redeploy after successful image publishing</label>'+
          '<div class="deployer-auto-fields">'+
            '<label>Publishing repository<select data-deployer-repo="'+name+'">'+repoChoices+'</select></label>'+
            '<label>Publishing workflow filename<input data-deployer-workflow="'+name+'" value="'+esc(workflow)+'" autocomplete="off" spellcheck="false"></label>'+
            '<label>Installed image<select data-deployer-image="'+name+'">'+imageChoices+'</select></label>'+
          '</div>'+
          '<div class="muted small">'+(isCustomApp?'Requires verified SHA-tagged GHCR images and permission to update custom Compose apps. No GitHub workflow secrets needed.':'Not available for catalog/wizard apps: use manual redeploy instead.')+'</div>'+
        '</div>')+
    '</div>';
  }).join('');
}

async function loadManagedDeployerApps(force=false){
  if(!els.deployerAppsList||deployerAppsBusy)return;
  if(!force&&Date.now()-deployerAppsLastLoaded<30000)return;
  deployerAppsBusy=true;
  if(!deployerAppsLastLoaded)els.deployerAppsList.textContent='Discovering TrueNAS apps…';
  try{
    const result=await j('/api/v1/deployer/apps');
    renderManagedDeployerApps(result.apps||[]);
    deployerAppsLastLoaded=Date.now();
    deployerAppsError='';
  }catch(e){
    deployerAppsError=String(e.message||e);
    els.deployerAppsList.textContent='App discovery unavailable: '+deployerAppsError+
      '. If the standalone deployer is on an older image, update it to the same release as RunnerHub.';
    deployerAppsLastLoaded=Date.now();
  }finally{
    deployerAppsBusy=false;
  }
}

function renderDeployerStatus(status,maintenance={}){
  if(!status||status.available===false){
    els.deployerSummary.textContent='Unavailable';
    els.deployerStatus.textContent=status?.detail||'Deployer bootstrap is unavailable on this RunnerHub instance.';
    if(els.deployerAppsList)els.deployerAppsList.textContent='Deployer unavailable.';
    return;
  }
  if(!status.configured){
    els.deployerSummary.textContent='Not configured';
    els.deployerStatus.innerHTML='Standalone redeployer is not installed. Open <strong>Enable automatic redeploy</strong> to install it.';
    if(els.deployerAppsList)els.deployerAppsList.textContent='Install the redeployer to discover apps.';
    return;
  }
  els.deployerSummary.textContent=maintenance.paused?'Draining CI':status.healthy?'Healthy':'Needs attention';
  els.deployerStatus.innerHTML=
    '<div class="repo-status"><span class="badge'+(status.healthy?'':' warn')+'">'+esc(status.healthy?'Deployer healthy':'Deployer unreachable')+'</span></div>'+
    '<div class="resource-summary">'+esc(status.deployerApp||'runnerhub-deployer')+' · '+esc(status.deployerUrl||'')+' · target '+esc(status.targetId||'runnerhub')+'</div>'+
    '<div class="muted small">'+esc(status.detail||'')+(status.configuredAt?' · configured '+esc(fmtTime(status.configuredAt)):'')+'</div>'+
    (maintenance.paused?'<div class="resource-note warn"><strong>RunnerHub maintenance active:</strong> new CI jobs are paused; '+esc(String(maintenance.activeWorkers||0))+' active worker(s) remain · '+esc(maintenance.phase||'draining')+'</div>':'');
  if(status.healthy)void loadManagedDeployerApps();
  else if(els.deployerAppsList)els.deployerAppsList.textContent='Deployer is currently unreachable.';
}

async function installDeployer(){
  const button=document.getElementById('install-deployer');
  const body={
    endpoint:document.getElementById('deployer-endpoint').value.trim(),
    port:Number(document.getElementById('deployer-port').value),
    username:document.getElementById('deployer-username').value.trim(),
    apiKey:document.getElementById('deployer-api-key').value.trim(),
    hostDataPath:document.getElementById('deployer-host-data-path').value.trim(),
    runnerHubApp:document.getElementById('deployer-runnerhub-app').value.trim(),
    healthUrl:document.getElementById('deployer-health-url').value.trim(),
    insecureSkipVerify:document.getElementById('deployer-insecure-tls').checked
  };
  if(!body.endpoint||!body.username||!body.apiKey||!body.healthUrl){
    return message('TrueNAS URL, service user, API key, and health URL are required.','error');
  }
  button.disabled=true;
  show(els.deployerTokenResult,false);
  try{
    message('Installing the standalone RunnerHub redeployer on TrueNAS…');
    const result=await j('/api/v1/deployer/install',{
      method:'POST',
      headers:{'content-type':'application/json'},
      body:JSON.stringify(body)
    });
    document.getElementById('deployer-api-key').value='';
    els.deployerWorkflowToken.textContent=result.workflowToken||'';
    show(els.deployerTokenResult,Boolean(result.workflowToken));
    message('Redeployer installed. Save the one-time workflow token as '+(result.workflowSecretName||'RUNNERHUB_DEPLOY_TOKEN')+' before closing this page.','success');
    deployerAppsLastLoaded=0;
    await refresh();
  }catch(e){
    message(e.message,'error');
  }finally{
    button.disabled=false;
  }
}

async function copyDeployerToken(){
  const token=els.deployerWorkflowToken.textContent.trim();
  if(!token)return;
  try{
    await navigator.clipboard.writeText(token);
    message('Deployment token copied.','success');
  }catch{
    message('Could not access the clipboard. Copy the token manually.','error');
  }
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

function loadFleetAlertFirstSeen(){
  const result=new Map();
  const now=Date.now();
  try{
    const saved=JSON.parse(localStorage.getItem('runnerhub-fleet-alert-first-seen-v1')||'[]');
    if(!Array.isArray(saved))return result;
    // Only retain recent and well-formed incident timestamps. Never trust
    // persisted data to claim an issue was observed indefinitely.
    for(const item of saved.slice(0,500)){
      if(!Array.isArray(item)||typeof item[0]!=='string'||
          !/^(host|listener|runtime|startup):[\w.-]{1,150}$/.test(item[0]))continue;
      const timestamp=item[1];
      if(typeof timestamp!=='number'||!Number.isFinite(timestamp)||
          timestamp>now||now-timestamp>24*60*60*1000)continue;
      result.set(item[0],timestamp);
    }
  }catch{/* Storage may be disabled, corrupt or from an older version. */}
  return result;
}
function persistFleetAlertFirstSeen(){
  try{localStorage.setItem('runnerhub-fleet-alert-first-seen-v1',JSON.stringify([...fleetAlertFirstSeen]))}
  catch{/* Work in memory when storage is unavailable. */}
}
function loadFleetAlertSilences(){
  try{
    const saved=JSON.parse(localStorage.getItem('runnerhub-fleet-alert-silences')||'[]');
    return new Map((Array.isArray(saved)?saved:[]).filter(x=>
      Array.isArray(x)&&typeof x[0]==='string'&&Number.isFinite(x[1])&&x[1]>Date.now()));
  }catch{return new Map()}
}
function detectFleetAlertCandidates(hosts,pools,workers,statuses,runtimes,now=Date.now()){
  const issues=[];
  for(const host of hosts||[]){
    if(!host.id)continue;
    const state=String(host.state||'').toLowerCase();
    if(state==='offline'||state==='unreachable')
      issues.push({id:'host:'+host.id,title:'Host '+(host.name||host.id)+' offline',
        detail:'Check agent connectivity and host reachability.',href:'/diagnostics.html'});
  }
  const listener=new Map((statuses||[]).map(x=>[x.poolId,x]));
  const runtime=new Map((runtimes||[]).map(x=>[x.poolId,x]));
  for(const pool of pools||[]){
    if(!pool.id||Number(pool.githubInstallationId)<=0)continue;
    const label=repoName(pool)+' / '+(pool.scaleSetName||'runnerhub');
    const state=String(listener.get(pool.id)?.state||'').toLowerCase();
    if(['error','backing_off'].includes(state))
      issues.push({id:'listener:'+pool.id,title:'Listener '+label+' '+state,
        detail:'Inspect GitHub API rate limits, listener authentication and startup evidence.',href:'/diagnostics.html'});
    const health=runtime.get(pool.id);
    if(health&&(health.state==='degraded'||health.healthy===false))
      issues.push({id:'runtime:'+pool.id,title:'Runtime '+label+' degraded',
        detail:'Host eligibility, worker provisioning, or registration may be blocking CI.',href:'/diagnostics.html'});
  }
  const recent=new Map();
  const cutoff=now-30*60*1000;
  for(const worker of workers||[]){
    if(worker.jobName||worker.jobResult||worker.workflowRunId)continue;
    if(!worker.failureReason||String(worker.state||'').toLowerCase()!=='failed')continue;
    const at=Date.parse(worker.failedAt||worker.updatedAt||'');
    if(!Number.isFinite(at)||at<cutoff||at>now)continue;
    const key=String(worker.poolId||'unknown');
    recent.set(key,(recent.get(key)||0)+1);
  }
  for(const [poolId,count] of recent){
    if(count<3)continue;
    issues.push({id:'startup:'+poolId,title:count+' recent runner startup failures',
      detail:'Multiple runners failed before a job started in the last 30 minutes. Review startup diagnostics.',href:'/diagnostics.html'});
  }
  return issues;
}
function renderFleetAlerts(hosts,pools,workers,statuses,runtime,fresh=true){
  const container=document.getElementById('fleet-alerts');
  if(!container)return;
  if(!fresh){
    // An outage is not evidence that a condition persisted throughout the gap.
    // Restart the observation window once fresh telemetry resumes.
    if(fleetAlertFirstSeen.size){
      fleetAlertFirstSeen.clear();
      persistFleetAlertFirstSeen();
    }
    container.innerHTML='<div class="muted small">Alert watch paused: some fleet data is stale. Sustained-alert timers will restart after refresh.</div>';
    return;
  }
  const now=Date.now();
  const issues=detectFleetAlertCandidates(hosts,pools,workers,statuses,runtime,now);
  const ids=new Set(issues.map(x=>x.id));
  let changed=false;
  for(const key of fleetAlertFirstSeen.keys())if(!ids.has(key)){
    fleetAlertFirstSeen.delete(key);
    changed=true;
  }
  for(const issue of issues)if(!fleetAlertFirstSeen.has(issue.id)){
    fleetAlertFirstSeen.set(issue.id,now);
    changed=true;
  }
  if(changed)persistFleetAlertFirstSeen();
  const sustained=issues.filter(x=>now-fleetAlertFirstSeen.get(x.id)>=5*60*1000);
  const visible=sustained.filter(x=>(fleetAlertSilences.get(x.id)||0)<=now);
  const muted=sustained.filter(x=>(fleetAlertSilences.get(x.id)||0)>now);
  const alertRow=(issue,isMuted)=>'<div class="fleet-alert-item"><div><strong>'+esc(issue.title)+'</strong><div class="muted small">'+
    esc(issue.detail)+(isMuted?' · Muted until '+esc(fmtTime(new Date(fleetAlertSilences.get(issue.id)).toISOString())):'')+
    '</div></div><div class="actions"><a class="button secondary compact" href="'+issue.href+'">Investigate</a>'+
    '<button type="button" class="secondary compact" data-fleet-alert-silence="'+esc(issue.id)+'" data-fleet-alert-action="'+(isMuted?'unmute':'mute')+'">'+(isMuted?'Unmute':'Mute 1h')+'</button></div></div>';
  const html='<div class="fleet-alert-heading"><strong>Alert watch</strong><span class="muted small">'+
    'Sustained conditions (5 min) · browser-only</span></div>'+
    (sustained.length?'<div class="fleet-alert-list">'+visible.slice(0,8).map(issue=>alertRow(issue,false)).join('')+
      muted.slice(0,8).map(issue=>alertRow(issue,true)).join('')+'</div>':
    '<div class="muted small">'+(issues.length?
      'Conditions are being observed; no sustained alerts yet.':'No sustained problems detected.')+'</div>');
  // Preserve keyboard focus when a background refresh does not change the UI.
  if(container.innerHTML!==html)container.innerHTML=html;
}
function renderFleetOverview(hosts,pools,workers,statuses,runtimeHealth,progress=[]){
  const allHosts=Array.isArray(hosts)?hosts:[];
  // Archived enrollments remain in the API for history, but are not fleet capacity.
  const currentHosts=allHosts.filter(h=>String(h.state||'').toLowerCase()!=='archived');
  const allPools=Array.isArray(pools)?pools:[];
  const allWorkers=Array.isArray(workers)?workers:[];
  const progressByWorker=new Map((Array.isArray(progress)?progress:[]).map(p=>[p.workerId,p]));
  const listenerByPool=new Map((statuses||[]).map(s=>[s.poolId,s]));
  const runtimeByPool=new Map((runtimeHealth||[]).map(s=>[s.poolId,s]));
  const online=currentHosts.filter(h=>String(h.state||'').toLowerCase()==='online').length;
  const active=allWorkers.filter(w=>!['deleted','completed','failed'].includes(String(w.state||'').toLowerCase()));
  const busy=active.filter(w=>String(w.state||'').toLowerCase()==='busy').length;
  const connected=allPools.filter(p=>p.scaleSetDisabled!==true&&(Number(p.githubInstallationId)>0||listenerByPool.get(p.id)?.state==='error'));
  const ready=connected.filter(p=>listenerByPool.get(p.id)?.state==='ready').length;
  const cards=[
    ['Hosts online',online+' / '+currentHosts.length,'Online compute machines','#compute-section'],
    ['Active runners',String(active.length),busy+' running jobs (GitHub queue separate)','#active-runners-section'],
    ['Listeners ready',ready+' / '+connected.length,'Connected scale-set listeners','#compute-section'],
    ['Runner capacity',String(connected.reduce((n,p)=>n+Math.max(0,Number(p.maxConcurrency)||0),0)),
      'Combined configured concurrency limit','#compute-section']
  ];
  let html='<div class="fleet-stats">'+cards.map(([name,value,detail,href])=>
    '<a class="fleet-stat" href="'+href+'"><span class="stat-label">'+esc(name)+'</span>'+
    '<strong>'+esc(value)+'</strong><span class="stat-detail">'+esc(detail)+'</span></a>'
  ).join('')+'</div>';
  const notices=[];
  for(const worker of allWorkers){
    const conflict=staleGitHubWorkerDetail(worker,progressByWorker.get(worker.id));
    if(conflict)notices.push('<strong>GitHub/RunnerHub state mismatch:</strong> '+esc(conflict)+
      ' <a href="#active-runners-section">Review runner</a>.');
  }
  if(!currentHosts.length)notices.push(allHosts.length?
    'No active machines registered. Archived enrollments are retained for history; add a machine under <a href="#compute-section">Compute</a>.':
    'No machines registered. Add a machine under <a href="#compute-section">Compute</a>.');
  else if(!online)notices.push('No machines online. Check agent connectivity and <a href="/diagnostics.html">diagnostics</a>.');
  else if(online<currentHosts.length)notices.push(esc(currentHosts.length-online)+' machine(s) not online; check <a href="#compute-section">Compute</a>.');
  if(!connected.length)notices.push('No connected runner pools. Connect a private repository under <a href="#repositories-section">Repositories</a>.');
  for(const pool of connected){
    const name=repoName(pool)+' / '+(pool.scaleSetName||'runnerhub');
    const listener=listenerByPool.get(pool.id);
    const runtime=runtimeByPool.get(pool.id);
    const publisherBlocker=publisherHostBlocker(pool,allHosts);
    const concurrencyBlocker=poolConcurrencyBlocker(pool,allWorkers);
    if(concurrencyBlocker)notices.push('<strong>'+esc(name)+'</strong>: '+esc(concurrencyBlocker)+' <a href="#active-runners-section">Review runners</a>.');
    if(publisherBlocker)notices.push('<strong>'+esc(name)+'</strong>: '+esc(publisherBlocker)+' <a href="#compute-section">Review hosts</a>.');
    if(listener?.state!=='ready')notices.push('<strong>'+esc(name)+'</strong>: listener '+esc(listener?.state||'unknown')+(listener?.detail?' — '+esc(listener.detail):'')+'. <a href="/diagnostics.html">Inspect readiness</a>.');
    else if(runtime?.state==='degraded'||runtime?.healthy===false)
      notices.push('<strong>'+esc(name)+'</strong>: runtime degraded. <a href="/diagnostics.html">Inspect failure</a>.');
    else if(!runtime||!['healthy','active'].includes(runtime.state))
      notices.push('<strong>'+esc(name)+'</strong>: runtime '+esc(runtime?.state||'unknown')+'. <a href="/diagnostics.html">Check health</a>.');
  }
  if(notices.length)html+='<div class="fleet-health-alert"><div class="fleet-health-header"><h3>Needs attention ('+notices.length+')</h3></div>'+
    '<ul class="fleet-health-list">'+notices.slice(0,6).map(n=>'<li>'+n+'</li>').join('')+'</ul>'+
    (notices.length>6?'<div class="resource-note muted">And '+(notices.length-6)+' more. Open Diagnostics for the full readiness report.</div>':'')+'</div>';
  else html+='<div class="fleet-health-ok">No known fleet or listener blockers reported. This does not guarantee that a specific queued GitHub job can be scheduled.</div>';
  els.fleetOverview.innerHTML=html;
}

function poolRecentQueueText(queue){
  if(!queue)return '—';
  const samples=Number(queue.samples)||0;
  const started=Number(queue.startedJobs)||0;
  const wait=samples>0?efficiencySeconds(queue.p95Seconds):'—';
  return wait+' · '+samples+'/'+started;
}

function renderPools(pools,statuses,runtimeHealth,recommendationByPool,hosts=[],workers=[],progress=[]){
  const listeners=new Map((statuses||[]).map(s=>[s.poolId,s]));
  const runtimes=new Map((runtimeHealth||[]).map(s=>[s.poolId,s]));
  const progressByWorker=new Map((progress||[]).map(p=>[p.workerId,p]));
  const rows=(pools||[]).map(p=>{
    const r=recommendationByPool.get(p.id);
    const listenerStatus=listeners.get(p.id);
    const listener=listenerStatus?.state||'unknown';
    const runtime=runtimes.get(p.id)?.state||'unknown';
    const label=p.scaleSetName||'runnerhub';
    const confirmed=publisherHostBlocker(p,hosts);
    const mismatch=(workers||[]).filter(w=>w.poolId===p.id)
      .map(w=>staleGitHubWorkerDetail(w,progressByWorker.get(w.id))).find(Boolean);
    const poolBusy=poolConcurrencyBlocker(p,workers);
    const capacity=r?.capacity;
    const capacityReason=capacity&&Number(capacity.additionalWorkers)===0&&
      Number(capacity.activeWorkers)<Number(p.maxConcurrency)?
      (capacity.reason||'No free capacity confirmed by the last pool summary.'):'';
    const listenerBlocker=listener==='error'?(listenerStatus?.detail||'Scale-set registration is blocked'):'';
    const diagnosis=mismatch||listenerBlocker||poolBusy||confirmed||capacityReason||'No known host eligibility blocker';
    return '<tr><td>'+esc(repoName(p))+'</td><td><code>'+esc(label)+'</code></td>'+
      '<td><span class="pool-status '+(listener==='ready'?'ok':listener==='unknown'?'unknown':'attention')+'">'+esc(listener)+'</span></td>'+
      '<td><span class="pool-status '+(['healthy','active'].includes(runtime)?'ok':runtime==='unknown'?'unknown':'attention')+'">'+esc(runtime)+'</span></td>'+
      '<td>'+esc(r?.capacity?String(r.capacity.activeWorkers??'—')+' / '+p.maxConcurrency:'—')+'</td>'+
      '<td>'+esc(cpuCores(p.resources?.cpuMillis))+' CPU · '+esc(gib(p.resources?.memoryBytes))+' GiB</td>'+
      '<td>'+esc(poolRecentQueueText(r?.latencies?.recentQueue))+'</td>'+
      '<td>'+esc(bottleneckLabel(r?.capacity?.bottleneck))+'</td>'+
      '<td class="pool-diagnosis">'+esc(diagnosis)+'</td></tr>';
  }).join('');
  const summary=(pools||[]).length
    ? '<div class="table-wrap pool-status-table"><table><thead><tr><th>Repository</th><th>Runner label</th><th>Listener</th><th>Runtime</th><th>Active / limit</th><th>Configured budget</th><th>Queue p95 (7d) · samples</th><th>Bottleneck</th><th>Host diagnosis</th></tr></thead><tbody>'+rows+'</tbody></table></div>'
    : '<div class="empty">No runner pools configured. Connect a repository to create one.</div>';
  const advanced='<details class="pool-advanced"><summary>Advanced pool metrics and resource learning</summary>'+tab(pools,[
    ['Repository',x=>repoName(x)],['Class',x=>runnerClass(x)],
    ['CPU / container',x=>cpuCores(x.resources?.cpuMillis)+' cores'],
    ['RAM / container',x=>gib(x.resources?.memoryBytes)+' GiB'],
    ['Eligible agents',x=>recommendationByPool.get(x.id)?.capacity?.compatibleHosts??'—'],
    ['Placement domains',x=>recommendationByPool.get(x.id)?.capacity?.placementDomains??'—'],
    ['7d queue p50',x=>efficiencySeconds(recommendationByPool.get(x.id)?.latencies?.recentQueue?.p50Seconds)],
    ['7d queue coverage',x=>{const q=recommendationByPool.get(x.id)?.latencies?.recentQueue;return q?efficiencyCoverage(q.samples,q.startedJobs):'—'}],
    ['Provision',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.provision)],
    ['Register',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.registration)],
    ['Execute',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.execution)],
    ['Cleanup',x=>fmtDuration(recommendationByPool.get(x.id)?.latencies?.cleanup)],
    ['CPU reserved',x=>reservedCPU(recommendationByPool.get(x.id)?.capacity)],
    ['RAM reserved',x=>reservedRAM(recommendationByPool.get(x.id)?.capacity)],
    ['Observed peak',x=>observedText(recommendationByPool.get(x.id))],
    ['Suggested size',x=>sizingText(recommendationByPool.get(x.id))],
    ['Learning',x=>learningText(recommendationByPool.get(x.id))],
    ['Auto-size',x=>x.autoTuneResources?'on':'off'],
    ['Can fit',x=>{const n=recommendationByPool.get(x.id)?.capacity?.additionalWorkers;return Number.isFinite(n)?'+'+n:'—'}],
    ['Reason',x=>recommendationByPool.get(x.id)?.capacity?.reason||'—'],
    ['Recommend',x=>recommendationByPool.get(x.id)?.recommended??'—']
  ])+'</details>';
  renderPreservingTableScroll(els.pools,summary+advanced);
}

async function refresh(force=false){
  if(refreshInFlight){
    refreshPending=true;
    refreshPendingForce=refreshPendingForce||force;
    return;
  }
  refreshInFlight=true;
  if(els.refreshNow)els.refreshNow.disabled=true;
  if(els.dataFreshness)els.dataFreshness.textContent='Refreshing…';
  try{
    await loadAuth();
    const canAPI=auth.authenticated||Boolean(els.token.value);
    const identity=auth.authenticated?'github:'+String(auth.user||''):
      (els.token.value?'token:'+els.token.value:'signed-out');
    if(identity!==dashboardIdentity){
      dashboardIdentity=identity;
      clearDashboardSnapshot();
    }
    if(!canAPI){
      els.hosts.innerHTML=els.pools.innerHTML=els.activeRunners.innerHTML=els.workers.innerHTML=els.audit.innerHTML=els.ciEfficiency.innerHTML='<span class="muted">Sign in to view.</span>';
      els.fleetOverview.innerHTML='<div class="empty">Sign in to view your runner fleet.</div>';
      show(els.fleetWarning,false);
      els.deployerSummary.textContent='';
      els.deployerStatus.innerHTML='<span class="muted">Sign in to view.</span>';
      if(els.deployerAppsList)els.deployerAppsList.textContent='Sign in to manage TrueNAS apps.';
      els.activeRunnerSummary.textContent='';
      els.workerSummary.textContent='';
      renderGitHubQueue();
      els.repos.innerHTML='';
      els.dataFreshness.textContent='Sign in required';
      clearDashboardSnapshot();
      return;
    }
    const errors=[];
    const collect=async(key,url,fallback)=>{
      try{const data=await j(url);snapshot[key]=data;return data}
      catch(e){errors.push(key+': '+String(e.message||e));return Object.hasOwn(snapshot,key)?snapshot[key]:fallback}
    };
    const requests=[
      collect('hosts','/api/v1/hosts',[]),
      collect('pools','/api/v1/pools',[]),
      collect('workers','/api/v1/workers?excludeDeleted=true',[]),
      collect('progress','/api/v1/workers/progress',[]),
      collect('audit','/api/v1/audit?limit=12',[]),
      collect('scalesets','/api/v1/scalesets/status',[]),
      collect('runtime','/api/v1/runtime-health',[]),
      collect('deployer','/api/v1/deployer/status',{available:false,detail:'Not available'}),
      collect('maintenance','/api/v1/maintenance',{paused:false,activeWorkers:0,phase:'unknown'})
    ];
    if(auth.authenticated){
      const inventoryDue=force||!snapshot.repos||!snapshot.installations||
        Date.now()-repositoryInventoryAt>60000;
      if(inventoryDue){
        requests.push(collect('repos','/api/v1/github/connected-repositories',[]));
        requests.push(collect('installations','/api/v1/github/installations',[]));
        repositoryInventoryAt=Date.now();
      }else{
        requests.push(Promise.resolve(snapshot.repos));
        requests.push(Promise.resolve(snapshot.installations));
      }
    }
    const [h,p,w,progress,a,statuses,runtimeHealth,deployer,maintenance,repos,installations]=await Promise.all(requests);
    const now=Date.now();
    const poolKey=(p||[]).map(pool=>String(pool.id)).sort().join(',');
    if(force||poolKey!==recommendationCache.key||now-recommendationCache.at>30000){
      const failures=[];
      const pairs=await Promise.all((p||[]).map(async pool=>{
        try{return [pool.id,await j('/api/v1/pools/'+encodeURIComponent(pool.id)+'/recommendation')]}
        catch(e){failures.push('recommendation '+repoName(pool)+': '+String(e.message||e));return [pool.id,recommendationCache.value.get(pool.id)||null]}
      }));
      recommendationCache={key:poolKey,at:now,value:new Map(pairs),errors:failures};
    }
    errors.push(...recommendationCache.errors);
    const connectedRepoNames=[...new Set((p||[])
      .filter(pool=>pool.scope?.kind==='repository'&&pool.scope.owner&&pool.scope.repository)
      .map(pool=>pool.scope.owner+'/'+pool.scope.repository))];
    const efficiencyKey=[...connectedRepoNames].sort().join(',');
    if(force||efficiencyKey!==efficiencyCache.key||now-efficiencyCache.at>60000){
      const entries=await Promise.all(connectedRepoNames.map(async repository=>{
        const [owner,name]=repository.split('/');
        try{return {repository,data:await j('/api/v1/repositories/'+encodeURIComponent(owner)+'/'+encodeURIComponent(name)+'/ci-efficiency?window=30d')}}
        catch(e){return {repository,error:String(e.message||e)}}
      }));
      const failures=entries.filter(item=>item.error);
      efficiencyCache={key:efficiencyKey,at:now,value:entries,
        errors:failures.length?[failures.length+' CI efficiency result(s) unavailable']:[]};
    }
    errors.push(...efficiencyCache.errors);
    const poolByID=new Map((p||[]).map(x=>[x.id,x]));
    const hostByID=new Map((h||[]).map(x=>[x.id,x]));
    const progressByWorker=new Map((progress||[]).map(x=>[x.workerId,x]));
    renderFleetOverview(h,p,w,statuses,runtimeHealth,progress);
    renderFleetAlerts(h,p,w,statuses,runtimeHealth,
      !errors.some(error=>/^(hosts|pools|workers|scalesets|runtime):/.test(error)));
    renderOnboardingChecklist(h,p,statuses,installations,repos);
    renderDeployerStatus(deployer,maintenance);
    renderCIEfficiency(efficiencyCache.value);
    renderHosts(h,w,poolByID);
    renderPools(p,statuses,runtimeHealth,recommendationCache.value,h,w,progress);
    renderActiveRunners(w,poolByID,hostByID,progressByWorker);
    renderGitHubQueue();
    renderWorkers(w,poolByID);
    renderPreservingTableScroll(els.audit,tab(a,[
      ['When',x=>fmtTime(x.createdAt)],
      ['Action',x=>x.action],
      ['Result',x=>x.result],
      ['Detail',x=>x.safeDetail||'']
    ]));
    if(auth.authenticated&&!els.repos.querySelector('.repo-config[open]'))
      renderRepos(repos||[],p||[],statuses||[],runtimeHealth||[],h||[],installations||[]);
    if(errors.length){
      els.fleetWarning.textContent='Some data could not be refreshed ('+errors.join(' · ')+'). Last successful values are shown where available; check Diagnostics if this continues.';
      show(els.fleetWarning,true);
    }else show(els.fleetWarning,false);
    if(location.hash==='#activity-section'&&
      (force||Date.now()-historyLastUpdated>30000))void loadHistory();
    els.dataFreshness.textContent=(errors.length?'Partial update · ':'Updated · ')+
      new Date().toLocaleTimeString([],{hour:'numeric',minute:'2-digit',second:'2-digit'});
  }catch(e){
    if(els.fleetWarning){
      els.fleetWarning.textContent='Refresh failed: '+String(e.message||e)+'. Previously loaded data may be stale.';
      show(els.fleetWarning,true);
    }
    if(els.dataFreshness)els.dataFreshness.textContent='Refresh failed';
  }finally{
    refreshInFlight=false;
    if(els.refreshNow)els.refreshNow.disabled=false;
    if(refreshPending){
      const pendingForce=refreshPendingForce;
      refreshPending=false;refreshPendingForce=false;
      void refresh(pendingForce);
    }
  }
}

const historyQueryFields=[
  ['repository','history-repo'],['state','history-state'],
  ['search','history-search'],['pool','history-pool'],['host','history-host'],
  ['from','history-from'],['to','history-to']
];
function updateHistoryFilterSuggestions(){
  const hosts=(snapshot.hosts||[]).filter(h=>h&&h.id);
  const pools=(snapshot.pools||[]).filter(p=>p&&p.id);
  const render=(id,items)=> {
    const el=document.getElementById(id);
    if(!el)return;
    const markup=items.map(item=>'<option value="'+esc(item.value)+'" label="'+esc(item.label)+'"></option>').join('');
    if(el.innerHTML!==markup)el.innerHTML=markup;
  };
  render('history-host-options',hosts.map(h=>({value:h.id,label:(h.name||h.id)+' ('+(h.state||'unknown')+')'})));
  render('history-pool-options',pools.map(p=>({value:p.id,label:(p.scaleSetName||p.name||p.id)+' ('+repoName(p)+')'})));
}
function historyDatesAreOrdered(){
  const from=document.getElementById('history-from').value;
  const to=document.getElementById('history-to').value;
  if(from&&to&&from>to){
    message('History start date must not be after the end date (UTC).','error');
    return false;
  }
  return true;
}
function restoreHistoryFiltersFromURL(){
  const params=new URLSearchParams(location.search||'');
  for(const [key,id] of historyQueryFields){
    document.getElementById(id).value=params.get('h_'+key)||'';
  }
  const raw=params.get('h_offset')||'0';
  const n=Number(raw);
  historyOffset=Number.isSafeInteger(n)&&n>=0&&n<=100000&&n%20===0?n:0;
}
function writeHistoryFiltersToURL(){
  const params=new URLSearchParams(location.search||'');
  for(const [key,id] of historyQueryFields){
    params.delete('h_'+key);
    const value=document.getElementById(id).value.trim();
    if(value)params.set('h_'+key,value);
  }
  params.delete('h_offset');
  if(historyOffset)params.set('h_offset',String(historyOffset));
  const query=params.toString();
  history.replaceState(null,'',(location.pathname||'/')+(query?'?'+query:'')+'#activity');
}

async function loadHistory(){
  updateHistoryFilterSuggestions();
  const button=document.getElementById('history-apply');
  const previous=document.getElementById('history-prev');
  const next=document.getElementById('history-next');
  const count=document.getElementById('history-count');
  const container=document.getElementById('history-results');
  if(!container||!(auth.authenticated||els.token.value))return;
  if(!historyDatesAreOrdered())return;
  const sequence=++historyRequestSequence;
  button.disabled=true;
  previous.disabled=next.disabled=true;
  count.textContent='Loading worker history…';
  const params=new URLSearchParams({
    limit:'20',offset:String(historyOffset),
    repository:document.getElementById('history-repo').value.trim(),
    state:document.getElementById('history-state').value,
    search:document.getElementById('history-search').value.trim()
  });
  for(const key of ['pool','host','from','to']){
    const value=document.getElementById('history-'+key).value.trim();
    if(value)params.set(key,value);
  }
  try{
    const page=await j('/api/v1/workers/history?'+params);
    if(sequence!==historyRequestSequence)return;
    const workers=Array.isArray(page.items)?page.items:[];
    const total=Number(page.total)||0;
    if(historyOffset>=total&&historyOffset>0){
      historyOffset=Math.max(0,Math.floor((total-1)/20)*20);
      writeHistoryFiltersToURL();
      return void loadHistory();
    }
    const byPool=new Map((snapshot.pools||[]).map(pool=>[pool.id,pool]));
    if(!workers.length){
      container.innerHTML='<div class="empty">No workers matched these filters. Change the query or reset filters.</div>';
    }else{
      renderPreservingTableScroll(container,tab(workers,[
        ['Updated',x=>fmtTime(workerTimestamp(x))],
        ['Repository',x=>x.repository||repoName(byPool.get(x.poolId))||'—'],
        ['Class',x=>runnerClass(byPool.get(x.poolId))],
        ['Job',x=>x.jobName||'startup'],
        ['State',x=>workerDisplayState(x)],
        ['Duration',x=>x.startedAt&&x.completedAt?
          fmtAgeBetween(x.startedAt,x.completedAt):'—'],
        ['CPU peak',x=>x.usage?.samples?usageCPU(x.usage.peakCpuMillis):'—'],
        ['RAM peak',x=>x.usage?.samples?usageRAM(x.usage.peakMemoryBytes):'—'],
        ['GitHub',x=>githubRunLink(x,byPool.get(x.poolId)),true],
        ['Failure',x=>x.failureReason||'—']
      ]));
    }
    count.textContent=(total?String(historyOffset+1)+'–'+String(historyOffset+workers.length):'0')+
      ' of '+total+' records';
    previous.disabled=historyOffset===0;
    next.disabled=!page.hasMore;
    historyLastUpdated=Date.now();
  }catch(error){
    if(sequence!==historyRequestSequence)return;
    container.innerHTML='<div class="empty">Worker history unavailable. Check the API connection and try again.</div>';
    count.textContent=String(error?.message||error);
  }finally{
    if(sequence===historyRequestSequence)button.disabled=false;
  }
}
function fmtAgeBetween(start,end){
  if(isZeroTime(start)||isZeroTime(end))return '—';
  const ms=Date.parse(end)-Date.parse(start);
  if(!Number.isFinite(ms)||ms<0)return '—';
  const secs=Math.floor(ms/1000);
  if(secs<60)return secs+'s';
  const minutes=Math.floor(secs/60);
  if(minutes<60)return minutes+'m '+(secs%60)+'s';
  const hours=Math.floor(minutes/60);
  return hours+'h '+(minutes%60)+'m';
}

function efficiencyPercent(value){
  if(value===null||value===undefined)return '—';
  const n=Number(value);
  return Number.isFinite(n)?n.toFixed(1)+'%':'—';
}

function evidenceAwareWastePercent(value,coverage,analyzer,jobs){
  const n=Number(value);
  if(!Number.isFinite(n))return '—';
  // A positive result is evidence of an analyzed finding; a zero is only
  // meaningful when the analyzer covered the requested window.
  if(Number(jobs||0)===0)return '—';
  if(n===0&&(!coverage?.analysisWindowComplete||analyzer?.state!=='healthy'))return '—';
  return efficiencyPercent(value);
}
function efficiencySeconds(value){
  if(value===null||value===undefined)return '—';
  const n=Number(value);
  if(!Number.isFinite(n)||n<0)return '—';
  if(n<1)return n.toFixed(2)+'s';
  if(n<60)return n.toFixed(n<10?1:0)+'s';
  if(n<3600)return (n/60).toFixed(n<600?1:0)+'m';
  return (n/3600).toFixed(n<36000?1:0)+'h';
}

function efficiencyCoverage(samples,total){
  const s=Number(samples||0);
  const t=Number(total||0);
  if(!Number.isFinite(t)||t<=0)return '—';
  return s+'/'+t+' · '+efficiencyPercent((s/t)*100);
}

function efficiencyAnalyzerLabel(analyzer){
  const state=String(analyzer?.state||'unavailable');
  if(state==='healthy')return 'Analyzer healthy';
  if(state==='degraded')return 'Analyzer degraded';
  if(state==='pending')return 'Analyzer pending';
  return 'Analyzer unavailable';
}

function efficiencyExclusionLabel(reason){
  const labels={
    'missing-workflow-metadata':'Missing workflow/job metadata',
    'unsupported-isolation':'Unsupported runner isolation',
    'unpinned-runner-image':'Runner image is not pinned',
    'missing-host-context':'Missing host architecture/context',
    'invalid-repository':'Invalid repository identity',
    'workflow-parse':'Workflow could not be parsed safely',
    'runtime-environment':'Reads opaque runtime environment',
    'mutable-action':'Uses a mutable action reference',
    'opaque-action':'Uses an action RunnerHub cannot model yet',
    'unresolved-context':'Referenced GitHub context is unavailable',
    'workflow-not-classified':'Workflow semantics are not classified',
    'side-effecting-workflow':'Publish/deploy workflow is not reusable validation'
  };
  return labels[reason]||reason;
}

function renderCIEfficiency(items){
  if(!els.ciEfficiency)return;
  if(!items.length){
    els.ciEfficiency.innerHTML='<div class="empty">Connect a repository to collect CI efficiency evidence.</div>';
    return;
  }
  const rows=items.map(item=>{
    if(item.error)return [item.repository,'—','—','—','—','—','error'];
    const d=item.data||{};
    const coverage=d.coverage||{};
    const jobs=Number(d.jobs||0);
    const validationCoverage=efficiencyCoverage(coverage.validationIdentityJobs,jobs);
    const queueSamples=Number(d.queue?.overall?.samples||0);
    const queueText=queueSamples
      ? efficiencySeconds(d.queue?.overall?.p95Seconds)+' · '+queueSamples+'/'+jobs
      : '— · 0/'+jobs;
    return [
      item.repository,
      String(jobs),
      validationCoverage,
      evidenceAwareWastePercent(d.waste?.provablyAvoidablePercent,coverage,d.analyzer,jobs),
      evidenceAwareWastePercent(d.waste?.confirmedPercent,coverage,d.analyzer,jobs),
      evidenceAwareWastePercent(d.waste?.potentialSavingsPercent,coverage,d.analyzer,jobs),
      queueText
    ];
  });
  let html='<div class="table-wrap"><table><thead><tr><th>Repository</th><th>Jobs</th><th>Validation identity</th><th>Provably avoidable</th><th>Confirmed waste</th><th>Potential</th><th>Queue p95 · samples</th></tr></thead><tbody>'+
    rows.map(row=>'<tr>'+row.map(cell=>'<td>'+esc(cell)+'</td>').join('')+'</tr>').join('')+
    '</tbody></table></div>';
  for(const item of items){
    if(item.error){
      html+='<div class="resource-note muted">'+esc(item.repository)+': '+esc(item.error)+'</div>';
      continue;
    }
    const d=item.data||{};
    const coverage=d.coverage||{};
    const analyzer=d.analyzer||{};
    const findings=Array.isArray(d.findings)?d.findings:[];
    const jobs=Number(d.jobs||0);
    const analyzerClass=analyzer.state==='degraded'?' warn':'';
    const windowText=coverage.analysisWindowComplete
      ? 'findings window covers selected period'
      : 'findings window does not fully cover selected period';
    html+='<details class="advanced-details"><summary>'+esc(item.repository)+' evidence · '+findings.length+' finding'+(findings.length===1?'':'s')+'</summary><div class="advanced-body">';
    html+='<div class="resource-note"><span class="badge'+analyzerClass+'">'+esc(efficiencyAnalyzerLabel(analyzer))+'</span>'+
      '<div class="muted small">'+
      'Queue timing '+esc(efficiencyCoverage(coverage.queueSamples,jobs))+
      ' · validation identity '+esc(efficiencyCoverage(coverage.validationIdentityJobs,jobs))+
      ' · '+esc(windowText)+
      (analyzer.lastSuccessAt?' · last success '+esc(fmtTime(analyzer.lastSuccessAt)):'')+
      '</div>'+
      (analyzer.lastError?'<div class="small"><strong>Analyzer:</strong> '+esc(analyzer.lastError)+'</div>':'')+
      '</div>';
    const exclusions=Object.entries(coverage.validationExclusions||{}).sort((a,b)=>Number(b[1])-Number(a[1]));
    if(exclusions.length||Number(coverage.validationPendingJobs||0)>0){
      html+='<div class="resource-note"><strong>Validation identity coverage</strong>'+
        '<div class="muted small">'+esc(String(coverage.validationIdentityJobs||0))+' reusable identities · '+
        esc(String(coverage.validationExcludedJobs||0))+' excluded · '+
        esc(String(coverage.validationPendingJobs||0))+' not analyzed yet</div>';
      if(exclusions.length){
        html+='<div class="small">'+exclusions.map(([reason,count])=>esc(String(count))+' '+esc(efficiencyExclusionLabel(reason))).join(' · ')+'</div>';
      }
      html+='</div>';
    }
    if(!findings.length){
      html+='<div class="muted small">'+esc(coverage.detail||'No deterministic findings in this window.')+'</div>';
    }else{
      for(const finding of findings){
        html+='<div class="resource-note"><strong>'+esc(finding.detectorId)+'</strong> · '+esc(finding.class)+
          (finding.workflowRunId?' · run '+esc(finding.workflowRunId):'')+
          '<div class="muted small">'+(finding.evidence||[]).map(esc).join(' · ')+'</div>'+
          (finding.suggestedFix?'<div class="small"><strong>Fix:</strong> '+esc(finding.suggestedFix)+'</div>':'')+
          '</div>';
      }
    }
    html+='</div></details>';
  }
  renderPreservingTableScroll(els.ciEfficiency,html);
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
    '<div class="resource-note muted">CPU/RAM are worker limits. On verified aggregate-capable Docker hosts, DinD profiles use one total logical-worker budget across the runner, daemon, and nested containers. Unsupported hosts keep a conservative two-envelope fallback. Auto-size uses only compatible telemetry; manual CPU/RAM changes start a fresh learning window. Limits apply to newly created runners.</div>'+
    '<div class="publisher-actions"><button data-resource-action="'+(connected?'update':'connect')+'" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+poolAttr+'>'+esc(action)+'</button>'+
      (connected?'<button class="secondary" data-learning-reset data-pool-id="'+esc(pool.id)+'" data-learning-label="standard runner">Reset learning</button>':'')+
    '</div>'+
  '</details>';
}

function publisherEditor(repo,pool,status,runtime,hostAvailable,standardConnected){
  const enabled=isPublisherEnabled(pool);
  const cpu=pool?.resources?.cpuMillis?cpuCores(pool.resources.cpuMillis):2.5;
  const memory=pool?.resources?.memoryBytes?gib(pool.resources.memoryBytes):8;
  const disk=pool?.resources?.diskBytes?gib(pool.resources.diskBytes):40;
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
      '<label>Publisher disk (GiB)<input data-publisher-field="disk" type="number" min="1" step="1" value="'+esc(disk)+'"></label>'+
      '<label>Max publishers<input data-publisher-field="concurrency" type="number" min="1" step="1" value="'+esc(max)+'"></label>'+
    '</div>'+
    '<label class="check-row"><input data-publisher-field="autotune" type="checkbox"'+(pool?.autoTuneResources?' checked':'')+'> Auto-size publisher CPU/RAM from observed jobs</label>'+
    '<div class="resource-note muted">Max publishers is an upper bound, not a guarantee of runnable capacity. Publisher CPU/RAM is the total budget per logical worker on verified aggregate-capable hosts; unsupported hosts keep the conservative two-envelope reservation. Auto-size learns only from verified aggregate jobs, so legacy per-container samples are not mixed. Each publisher reserves the configured disk on one Docker-in-Docker capable host; set it from measured build usage and available host storage, not just concurrency.</div>';
  let buttons='';
  if(enabled){
    buttons='<div class="publisher-actions"><button data-publisher-action="update" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Save publisher settings</button><button class="secondary" data-learning-reset data-pool-id="'+esc(pool.id)+'" data-learning-label="publisher runner">Reset learning</button><button class="secondary" data-publisher-action="disable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Disable publisher</button></div>';
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

function vercelProfileEditor(repo,pool,status,runtime,hostAvailable,standardConnected){
  const enabled=Boolean(pool)&&Number(pool.githubInstallationId)>0;
  const listener=enabled?(status?.state||'pending'):'disabled';
  const runtimeState=enabled?(runtime?.state||'unknown'):'disabled';
  let badge='<span class="badge neutral">Vercel runner disabled</span>';
  if(enabled){
    const listenerClass=listener==='ready'?'':' warn';
    const runtimeClass=(runtimeState==='healthy'||runtimeState==='active')?'':' warn';
    badge='<span class="badge'+listenerClass+'">Vercel '+esc(listener)+'</span><span class="badge'+runtimeClass+'">Runtime '+esc(runtimeState)+'</span>';
  }else if(!hostAvailable){
    badge='<span class="badge warn">No online machine tagged '+esc(VERCEL_MACHINE_TAG)+'</span>';
  }
  let note='Runs Vercel CLI deployments on <code>runs-on: '+VERCEL_PROFILE_NAME+'</code> using standard <code>container-trusted</code> isolation. Jobs are restricted to machines tagged <code>'+VERCEL_MACHINE_TAG+'</code>; no Docker socket or privileged worker is used.';
  if(!standardConnected)note='Connect the standard runner first, then enable the Vercel deploy runner.';
  else if(!hostAvailable&&!enabled)note+=' Add the <code>'+VERCEL_MACHINE_TAG+'</code> tag to an online RunnerHub machine first.';
  const canEnable=standardConnected&&(hostAvailable||enabled);
  const required='<div class="resource-note muted"><strong>GitHub configuration:</strong> secret <code>VERCEL_TOKEN</code>; variables <code>VERCEL_ORG_ID</code> and <code>VERCEL_PROJECT_ID</code>. Prefer a protected <code>production</code> Environment. RunnerHub never stores the Vercel token. Vercel CLI is pinned to <code>'+esc(VERCEL_CLI_VERSION)+'</code>.</div>';
  const workflowBlock='<details class="workflow-template"><summary>Copy production workflow</summary>'+required+'<textarea readonly rows="20" spellcheck="false">'+esc(vercelWorkflowTemplate)+'</textarea><button class="secondary compact" data-vercel-copy>Copy workflow</button></details>';
  const action=enabled
    ? '<div class="publisher-actions"><button data-vercel-action="enable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Repair / reapply preset</button><button class="secondary" data-learning-reset data-pool-id="'+esc(pool.id)+'" data-learning-label="Vercel deploy runner">Reset learning</button><button class="secondary" data-vercel-action="disable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Disable Vercel runner</button></div>'
    : '<button data-vercel-action="enable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+(canEnable?'':' disabled')+'>Enable Vercel deploy runner</button>';
  return '<details class="repo-config vercel-config"><summary>Vercel deploy runner</summary><div class="repo-status">'+badge+'</div><div class="resource-note">'+note+'</div><div class="resource-summary">2 CPU · 4 GiB RAM · max 1 · machine tag '+esc(VERCEL_MACHINE_TAG)+'</div>'+action+workflowBlock+'</details>';
}

function runnerProfileEditor(repo,profiles,standardConnected){
  profiles=(profiles||[]).filter(p=>!isVercelProfile(p));
  const active=profiles.filter(p=>Number(p.githubInstallationId)>0);
  const existing=(profiles||[]).map(p=>{
    const tags=Object.keys(p.hostSelector||{}).join(', ')||'—';
    const enabled=Number(p.githubInstallationId)>0;
    const mode=p.isolation==='container-dind'?'Docker-capable':p.isolation==='host-persistent'?'Native host (trusted)':'standard';
    return '<div class="profile-row"><div><code>runs-on: '+esc(p.scaleSetName)+'</code><div class="muted small">machines: '+esc(tags)+' · '+esc(mode)+' · '+esc(cpuCores(p.resources?.cpuMillis))+' CPU · '+esc(gib(p.resources?.memoryBytes))+' GiB</div></div>'+
      (enabled?'<div class="publisher-actions"><button class="secondary compact" data-learning-reset data-pool-id="'+esc(p.id)+'" data-learning-label="'+esc(p.scaleSetName)+'">Reset learning</button><button class="secondary compact" data-profile-action="disable" data-profile-name="'+esc(p.scaleSetName)+'" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'">Disable</button></div>':'<span class="badge neutral">disabled</span>')+
    '</div>';
  }).join('');
  const disabled=standardConnected?'':' disabled';
  return '<details class="repo-config profile-config">'+
    '<summary>Custom runner profiles'+(active.length?' ('+active.length+')':'')+'</summary>'+
    '<div class="resource-note muted">Native profiles run workflow code on the host. They are only for trusted private repositories and require a dedicated non-admin agent account. Windows jobs are pinned to system-reported Windows amd64 hosts; this does not provide VM isolation or make public/fork workflows safe.</div>'+
    '<button type="button" class="secondary compact" data-android-emulator-preset'+disabled+'>Fill Android emulator profile</button>'+
    '<button type="button" class="secondary compact" data-windows-native-preset'+disabled+'>Fill Windows MSVC profile</button>'+
    '<div class="resource-note muted">Tags are hard placement constraints. Use host priority for normal scheduling preference; use a custom profile when a workflow must explicitly target a machine class such as <code>runnerhub-fast</code>. Verified aggregate-capable hosts enforce one total CPU/RAM budget per DinD worker; unsupported hosts keep conservative two-envelope reservations. DinD auto-sizing uses only verified aggregate samples.</div>'+
    (existing?'<div class="profile-list">'+existing+'</div>':'')+
    '<div class="resource-grid">'+
      '<label>Profile name<input data-profile-field="name" placeholder="fast"'+disabled+'></label>'+
      '<label>Required machine tags<input data-profile-field="tags" placeholder="fast"'+disabled+'></label>'+
      '<label>Mode<select data-profile-field="isolation"'+disabled+'><option value="container-trusted">Standard</option><option value="container-dind">Docker-capable</option><option value="host-persistent">Native host (trusted only)</option></select></label>'+
      '<label>CPU / runner<input data-profile-field="cpu" type="number" min="0.25" step="0.25" value="2.5"'+disabled+'></label>'+
      '<label>RAM / runner (GiB)<input data-profile-field="memory" type="number" min="1" step="0.5" value="4"'+disabled+'></label>'+
      '<label>Max concurrent<input data-profile-field="concurrency" type="number" min="1" step="1" value="2"'+disabled+'></label>'+
    '</div>'+
    '<label class="check-row"><input data-profile-field="autotune" type="checkbox"'+disabled+'> Auto-size CPU/RAM from observed jobs</label>'+
    '<button data-profile-action="enable" data-repo="'+esc(repo.fullName)+'" data-installation="'+esc(repo.installationId)+'"'+disabled+'>Create / update profile</button>'+
  '</details>';
}

function renderRepos(repos,pools,statuses,runtimeHealth,hosts,installations){
  lastRepoRenderArgs=[repos,pools,statuses,runtimeHealth,hosts,installations];
  renderOrganizationPoolSetup(repos,pools,statuses,installations);
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
  const vercelHostAvailable=(hosts||[]).some(h=>h.state==='online'&&String((h.tags||{})[VERCEL_MACHINE_TAG]||'').toLowerCase()==='true');
  const installationByAccount=new Map((installations||[]).map(x=>[String(x.account||'').toLowerCase(),x]));
  const hasPersonalInstallation=(installations||[]).some(x=>String(x.accountType||'').toLowerCase()==='user');
  const installedAccountText=(installations||[]).length
    ? (installations||[]).map(x=>'@'+String(x.account||'')+' · '+(String(x.accountType||'').toLowerCase()==='user'?'personal':'organization')).join(' · ')
    : 'No GitHub App installations detected.';
  const accountNames=[...new Set([
    ...(installations||[]).map(x=>String(x.account||'')).filter(Boolean),
    ...(repos||[]).map(x=>String(x.account||x.fullName?.split('/')[0]||'')).filter(Boolean)
  ])].sort((a,b)=>a.toLowerCase().localeCompare(b.toLowerCase()));
  const accountToggles=accountNames.length
    ? '<div class="repo-account-filters"><span class="muted small">Visible accounts</span>'+accountNames.map(account=>
        '<label class="repo-filter-chip"><input type="checkbox" data-repo-account-toggle="'+esc(account)+'"'+(repoAccountVisible(account)?' checked':'')+'> @'+esc(account)+'</label>'
      ).join('')+'</div>'
    : '';
  const accountHint='<div class="repo-account-summary"><div><strong>GitHub App accounts</strong><div class="muted small">'+esc(installedAccountText)+'</div>'+accountToggles+'</div>'+
    (!hasPersonalInstallation?'<a class="button secondary compact" href="/auth/github/install">Add personal account</a>':'')+
    '</div>';

  if(!repos.length){
    els.repos.innerHTML=accountHint+'<div class="empty">No repositories are available through the installed GitHub App accounts. Use <strong>Add / manage accounts</strong> to install RunnerHub on your personal account or grant repository access.</div>';
    return;
  }

  const sorted=[...repos].sort((a,b)=>{
    const aa=String(a.account||a.fullName?.split('/')[0]||'').toLowerCase();
    const ba=String(b.account||b.fullName?.split('/')[0]||'').toLowerCase();
    if(aa!==ba)return aa.localeCompare(ba);
    const ac=poolsByRepo.get(a.fullName.toLowerCase())?.standard?1:0;
    const bc=poolsByRepo.get(b.fullName.toLowerCase())?.standard?1:0;
    if(ac!==bc)return bc-ac;
    return a.fullName.localeCompare(b.fullName);
  });
  const search=String(els.repoSearch?.value||'').trim().toLowerCase();
  const showPublicUnsupported=els.repoShowPublic?.checked??true;
  const visibleRepos=sorted.filter(repo=>{
    const account=String(repo.account||repo.fullName?.split('/')[0]||'unknown');
    if(!repoAccountVisible(account))return false;
    const entry=poolsByRepo.get(repo.fullName.toLowerCase())||{standard:null};
    if(!showPublicUnsupported&&!repo.private&&!entry.standard)return false;
    if(!search)return true;
    const haystack=[
      repo.fullName,account,repo.defaultBranch,
      repo.private?'private':'public',
      entry.standard?'connected':(!repo.private?'unsupported':'not connected')
    ].join(' ').toLowerCase();
    return haystack.includes(search);
  });

  function renderRepoCard(repo){
    const entry=poolsByRepo.get(repo.fullName.toLowerCase())||{standard:null,publisher:null,profiles:[]};
    const pool=entry.standard;
    const publisher=entry.publisher;
    const vercelProfile=entry.profiles.find(isVercelProfile)||null;
    const status=pool?statusByPool.get(pool.id):null;
    const runtime=pool?runtimeByPool.get(pool.id):null;
    const publisherStatus=publisher?statusByPool.get(publisher.id):null;
    const publisherRuntime=publisher?runtimeByPool.get(publisher.id):null;
    const vercelStatus=vercelProfile?statusByPool.get(vercelProfile.id):null;
    const vercelRuntime=vercelProfile?runtimeByPool.get(vercelProfile.id):null;
    const privacy=repo.private?'Private':'Public';
    const learningPoolIds=[pool,publisher,...entry.profiles].filter(Boolean).map(p=>p.id);
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
        (repo.private?'<label class="check-row"><input type="checkbox" data-bulk-repo="'+esc(repo.fullName)+'"'+(bulkSelected.has(repo.fullName.toLowerCase())?' checked':'')+'> Select for bulk onboarding</label>':'')+
        '<div class="repo-title"><div><div class="repo-name">'+esc(repo.fullName)+'</div><div class="repo-meta muted">'+esc(privacy)+' · '+esc(repo.defaultBranch||'')+'</div></div></div>'+
        statusHtml+detail+
        (learningPoolIds.length?'<button class="secondary compact" data-repo-learning-reset="'+esc(learningPoolIds.join(','))+'" data-repo-name="'+esc(repo.fullName)+'">Reset all learning</button>':'')+
      '</div>'+
      (repo.private?resourceEditor(repo,pool):'')+
      (repo.private?vercelProfileEditor(repo,vercelProfile,vercelStatus,vercelRuntime,vercelHostAvailable,Boolean(pool)):'')+
      (repo.private?runnerProfileEditor(repo,entry.profiles,Boolean(pool)):'')+
      (repo.private?publisherEditor(repo,publisher,publisherStatus,publisherRuntime,publisherHostAvailable,Boolean(pool)):'')+
      '</article>';
  }

  const grouped=new Map();
  for(const repo of visibleRepos){
    const account=String(repo.account||repo.fullName?.split('/')[0]||'unknown');
    const key=account.toLowerCase();
    const group=grouped.get(key)||{account,repos:[]};
    group.repos.push(repo);
    grouped.set(key,group);
  }

  const groups=[...grouped.values()].map(group=>{
    const installation=installationByAccount.get(group.account.toLowerCase());
    const accountType=String(installation?.accountType||group.repos[0]?.accountType||'').toLowerCase();
    const label=accountType==='user'?'Personal account':accountType==='organization'?'Organization':'GitHub account';
    return '<section class="repo-account-group">'+
      '<div class="repo-account-head"><div><h3>@'+esc(group.account)+'</h3><div class="muted small">'+esc(label)+' · '+group.repos.length+' visible repositor'+(group.repos.length===1?'y':'ies')+'</div></div></div>'+
      '<div class="repo-account-grid">'+group.repos.map(renderRepoCard).join('')+'</div>'+
      '</section>';
  }).join('');

  const count='<div class="repo-filter-summary muted small">Showing '+visibleRepos.length+' of '+repos.length+' repositories</div>';
  const empty=visibleRepos.length?'':'<div class="empty">No repositories match the current search and visibility filters.</div>';
  els.repos.innerHTML=accountHint+count+groups+empty;
  updateBulkCount();
}

function renderOrganizationPoolSetup(repos,pools,statuses,installations){
  const accountSelect=document.getElementById('org-pool-account');
  const groupSelect=document.getElementById('org-pool-group');
  const repoSelect=document.getElementById('org-pool-repositories');
  const existing=document.getElementById('org-pool-existing');
  if(!accountSelect||!groupSelect||!repoSelect||!existing)return;
  organizationPoolInstallations=(installations||[]).filter(x=>String(x.accountType||'').toLowerCase()==='organization');
  organizationPoolRepositories=(repos||[]).filter(x=>String(x.accountType||'').toLowerCase()==='organization'&&x.private);
  organizationPoolRecords=new Map((pools||[]).filter(p=>p.scope?.kind==='organization').map(p=>[p.id,p]));
  const selected=accountSelect.value||organizationPoolInstallations[0]?.account||'';
  accountSelect.innerHTML=organizationPoolInstallations.map(x=>'<option value="'+esc(x.account)+'" data-installation="'+esc(x.installationId)+'">@'+esc(x.account)+'</option>').join('');
  if(organizationPoolInstallations.some(x=>String(x.account).toLowerCase()===String(selected).toLowerCase()))accountSelect.value=selected;
  else accountSelect.selectedIndex=0;
  const org=accountSelect.value;
  const installation=organizationPoolInstallations.find(x=>String(x.account).toLowerCase()===String(org).toLowerCase());
  const allowed=organizationPoolRepositories.filter(x=>String(x.account||x.fullName?.split('/')[0]||'').toLowerCase()===String(org).toLowerCase()&&Number(x.installationId)===Number(installation?.installationId));
  const prior=new Set([...(repoSelect.selectedOptions||[])].map(x=>x.value.toLowerCase()));
  repoSelect.innerHTML=allowed.map(x=>'<option value="'+esc(x.fullName)+'"'+(prior.has(String(x.fullName).toLowerCase())?' selected':'')+'>'+esc(x.fullName)+'</option>').join('');
  if(!allowed.length)repoSelect.innerHTML='<option disabled>No private repositories are available in this installation</option>';
  if(!organizationPoolLoadedAccount||organizationPoolLoadedAccount.toLowerCase()!==String(org).toLowerCase()){
    organizationPoolLoadedAccount=String(org||'');
    groupSelect.disabled=true;
    groupSelect.innerHTML='<option value="">Loading restricted groups…</option>';
    if(org)loadOrganizationRunnerGroups();
    else groupSelect.innerHTML='<option value="">Install RunnerHub on an organization first</option>';
  }
  const statusByPool=new Map((statuses||[]).map(s=>[s.poolId,s]));
  const configured=[...organizationPoolRecords.values()].map(p=>{
    const status=statusByPool.get(p.id)?.state||'unknown';
    const action=p.scaleSetDisabled?'Enable':'Disable';
    return '<div class="profile-row"><div><strong>@'+esc(p.scope.owner)+' / '+esc(p.scaleSetName||'runnerhub')+'</strong><div class="muted small">'+esc((p.allowedRepositories||[]).length)+' private repositories · group '+esc(p.runnerGroupId)+' · '+esc(status)+'</div></div><button type="button" class="secondary compact" data-org-pool-toggle="'+esc(p.id)+'">'+action+'</button></div>';
  }).join('');
  existing.innerHTML=configured||'<div class="muted small">No shared organization pools configured.</div>';
}

async function loadOrganizationRunnerGroups(){
  const account=document.getElementById('org-pool-account')?.value||'';
  const select=document.getElementById('org-pool-group');
  if(!select)return;
  if(!account){select.disabled=true;select.innerHTML='<option value="">Choose an organization first</option>';return}
  select.disabled=true;
  try{
    const groups=await j('/api/v1/github/runner-groups?org='+encodeURIComponent(account));
    const safe=(groups||[]).filter(g=>String(g.visibility).toLowerCase()==='selected'&&!g.allows_public_repositories);
    select.innerHTML=safe.map(g=>'<option value="'+esc(g.id)+'">'+esc(g.name)+' · selected repositories only</option>').join('')||'<option value="">No restricted runner groups available</option>';
    select.disabled=!safe.length;
  }catch(e){select.innerHTML='<option value="">Could not verify organization runner groups</option>';message(e.message,'error')}
}

async function saveOrganizationPool(enabled,pool){
  const status=document.getElementById('org-pool-status');
  const account=document.getElementById('org-pool-account');
  const installation=organizationPoolInstallations.find(x=>String(x.account).toLowerCase()===String(account?.value||'').toLowerCase());
  const repositories=pool?(pool.allowedRepositories||[]):[...document.getElementById('org-pool-repositories').selectedOptions].map(x=>x.value);
  const body={
    organization:pool?.scope?.owner||account?.value||'',
    installationId:Number(pool?.githubInstallationId||installation?.installationId),
    runnerGroupId:Number(pool?.runnerGroupId||document.getElementById('org-pool-group').value),
    scaleSetName:pool?.scaleSetName||document.getElementById('org-pool-name').value.trim()||'runnerhub-org',
    allowedRepositories:repositories,
    allowedWorkflows:pool?.allowedWorkflows||document.getElementById('org-pool-workflows').value.split(',').map(x=>x.trim()).filter(Boolean),
    maxConcurrency:Number(pool?.maxConcurrency||document.getElementById('org-pool-concurrency').value||2),
    isolation:pool?.isolation||document.getElementById('org-pool-isolation').value||'container-trusted',
    resources:pool?.resources||{cpuMillis:2500,memoryBytes:4294967296,diskBytes:21474836480},
    enabled
  };
  if(!body.organization||!body.installationId||!body.runnerGroupId||!body.allowedRepositories.length){
    if(status)status.textContent='Choose an organization, restricted runner group, and at least one private repository.';
    return;
  }
  const button=document.getElementById('org-pool-save');if(button)button.disabled=true;
  try{
    await j('/api/v1/github/organization-pools',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    if(status)status.textContent=enabled?'Shared organization pool saved. Wait for listener readiness, then run a real smoke workflow.':'Shared organization listener disabled; active jobs can drain.';
    await refresh();
  }catch(e){if(status)status.textContent=e.message}
  finally{if(button)button.disabled=false}
}
function loadMachineBootstrap(){
  try{
    const value=JSON.parse(sessionStorage.getItem(MACHINE_BOOTSTRAP_KEY)||'null');
    if(!value||typeof value!=='object'||!['working','unknown','failed','blocked','succeeded'].includes(value.state))return null;
    // A reload never proves a request completed. Reconnect by operation ID.
    return {...value,state:value.state==='working'?'unknown':value.state};
  }catch{return null}
}
function setMachineBootstrap(state,fields={}){
  // Explicit allowlist prevents callers from persisting SSH credentials.
  machineBootstrap={
    state,name:String(fields.name||''),startedAt:fields.startedAt||new Date().toISOString(),
    hostId:fields.hostId||'',operationId:fields.operationId||''
  };
  try{sessionStorage.setItem(MACHINE_BOOTSTRAP_KEY,JSON.stringify(machineBootstrap))}catch{}
  renderMachineBootstrap();
}
function machineStepLabel(step){
  return ({
    'request-accepted':'Request accepted',
    'preparing-enrollment':'Preparing enrollment',
    'verifying-host-key':'Verifying SSH host key',
    'configuring-remote-host':'Configuring Docker and agent',
    'waiting-for-agent':'Waiting for agent heartbeat',
    'cleaning-enrollment-secret':'Removing temporary enrollment secret',
    'agent-ready':'Agent confirmed online',
    'bootstrap-failed':'Remote setup failed',
    'bootstrap-timed-out':'Setup timed out',
    'enrollment-cleanup-failed':'Enrollment needs cleanup',
    'server-restarted':'Control plane restarted',
    'outcome-unknown':'Outcome unknown'
  })[step]||'Checking operation';
}
function renderMachineBootstrap(){
  const panel=document.getElementById('machine-bootstrap-status');
  if(!panel)return;
  if(!machineBootstrap){panel.innerHTML='';return}
  const status=machineBootstrap;
  const op=machineOperation?.id===status.operationId?machineOperation:null;
  const labels={
    working:'Enrollment in progress',unknown:'Enrollment status unconfirmed',
    succeeded:'Enrollment confirmed',failed:'Bootstrap failed',blocked:'Enrollment needs action'
  };
  const summary=op?.detail||({
    working:'Waiting for RunnerHub to acknowledge the operation; do not resubmit.',
    unknown:'Refreshing operation status. If no server record is available, inspect the host before retrying.',
    succeeded:'The agent heartbeat was confirmed.',
    failed:'The remote setup failed. Check the agent logs before retrying.',
    blocked:'An agent was observed, but a post-enrollment step failed. Inspect the remote machine before retrying.'
  })[status.state];
  const stage=op?'<div class="host-meta"><strong>Current stage:</strong> '+esc(machineStepLabel(op.step))+
    ' · Last confirmed progress '+esc(fmtAge(op.lastProgressAt))+' ago</div>':'';
  const timeline=Array.isArray(op?.events)&&op.events.length
    ? '<ol class="machine-timeline">'+op.events.slice(-10).map(e=>'<li>'+esc(machineStepLabel(e.step))+
      ' <span class="muted">'+esc(fmtTime(e.at))+'</span></li>').join('')+'</ol>'
    : '';
  panel.innerHTML='<div class="machine-progress-head"><strong>'+esc(labels[status.state])+
    '</strong><button type="button" class="secondary compact" data-machine-bootstrap-dismiss>Dismiss</button></div>'+
    '<div class="host-meta">Machine: '+esc(status.name||'unknown')+
    ' · Started '+esc(fmtAge(status.startedAt))+' ago'+
    (status.hostId?' · Host ID <code>'+esc(status.hostId)+'</code>':'')+'</div>'+
    stage+'<div class="resource-note">'+esc(summary)+'</div>'+timeline;
}
async function refreshMachineOperation(){
  const id=machineBootstrap?.operationId;
  if(!id||machineOperationPolling)return;
  machineOperationPolling=true;
  try{
    const op=await j('/api/v1/operations/'+encodeURIComponent(id));
    if(machineBootstrap?.operationId!==id)return;
    machineOperation=op;
    const state=['queued','working','waiting'].includes(op.state)?'working':
      ['succeeded','failed','blocked'].includes(op.state)?op.state:'unknown';
    const previous=machineBootstrap.state;
    setMachineBootstrap(state,{operationId:op.id,name:op.target,
      startedAt:op.startedAt,hostId:op.hostId});
    if(previous!=='succeeded'&&state==='succeeded')await refresh(true);
  }catch{
    // A network error says nothing about the remote machine's actual state.
    if(machineBootstrap?.operationId===id){
      machineOperation=null;
      setMachineBootstrap('unknown',{...machineBootstrap});
    }
  }finally{machineOperationPolling=false}
}
async function refreshRecentOperations(){
  const panel=document.getElementById('machine-operations');
  if(!panel)return;
  try{
    const ops=await j('/api/v1/operations?limit=10');
    if(!Array.isArray(ops))throw new Error('Invalid operations response');
    const cards=ops.map(op=>'<div class="machine-operation-row"><div><strong>'+esc(op.target)+
      '</strong> · '+esc(op.state)+' · '+esc(machineStepLabel(op.step))+
      '<div class="host-meta">Updated '+esc(fmtAge(op.updatedAt))+' ago'+
      (op.hostId?' · Host '+esc(op.hostId):'')+'</div></div>'+
      '<button type="button" class="secondary compact" data-operation-view="'+esc(op.id)+
      '" data-operation-name="'+esc(op.target)+'">Details</button></div>').join('');
    if(panel.innerHTML!==cards)panel.innerHTML=cards||'<div class="muted small">No tracked enrollments yet.</div>';
  }catch{
    panel.innerHTML='<div class="muted small">Operation history unavailable. Check your admin session or server connection.</div>';
  }
}
function renderMachineDuplicateWarning(hosts=snapshot.hosts){
  const panel=document.getElementById('machine-duplicate-warning');
  if(!panel)return;
  const name=document.getElementById('machine-name').value.trim().toLowerCase();
  const matches=(Array.isArray(hosts)?hosts:[]).filter(h=>
    name&&String(h.state||'').toLowerCase()!=='archived'&&String(h.name||'').trim().toLowerCase()===name);
  panel.innerHTML=matches.length
    ? '<strong>Existing host name:</strong> '+matches.map(h=>esc(h.name)+' ('+esc(h.state)+
      ', ID <code>'+esc(h.id)+'</code>)').join(', ')+
      '. This may be a previous enrollment or a distinct machine. Check IDs before creating another record.'
    : '';
}
function setupHostAndroid(button){
  const host=(snapshot.hosts||[]).find(h=>String(h.id)===button.dataset.hostAndroid);
  if(!host||String(host.state).toLowerCase()!=='online'||String(host.provider).toLowerCase()!=='docker'){
    return message('A verified online Docker host is required. Refresh and try again.','error');
  }
  document.getElementById('machine-mode').value='android-emulator';
  document.getElementById('machine-name').value=host.name||'';
  document.getElementById('machine-tags').value='android-emulator';
  document.getElementById('machine-priority').value=String(host.priority||0);
  for(const id of ['machine-host-fingerprint','machine-password','machine-sudo-password','machine-private-key','machine-registry-password'])document.getElementById(id).value='';
  chooseMachineMode();
  renderMachineDuplicateWarning();
  const panel=document.querySelector('.machine-add');
  if(panel){panel.open=true;panel.scrollIntoView?.({behavior:'smooth',block:'start'})}
  message('Setting up a separate Android KVM agent for '+host.name+'. Verify SSH address, user and fingerprint. The Docker agent stays online.','info');
  document.getElementById('machine-address').focus?.();
}
function recoverHost(button){
  const host=(snapshot.hosts||[]).find(h=>String(h.id)===button.dataset.hostRecover);
  if(!host)return message('Host is no longer in the current fleet snapshot. Refresh and try again.','error');
  const isAndroid=String(host.provider||'').toLowerCase()==='process' &&
    host.systemTags?.['workload.android-emulator-only']==='true';
  document.getElementById('machine-mode').value=isAndroid?'android-emulator':'docker';
  document.getElementById('machine-name').value=host.name||'';
  document.getElementById('machine-tags').value=hostTagText(host);
  document.getElementById('machine-priority').value=String(host.priority||0);
  chooseMachineMode();
  // Credentials and fingerprints are not transferable between host identities.
  for(const id of ['machine-host-fingerprint','machine-password','machine-sudo-password',
    'machine-private-key','machine-registry-password'])document.getElementById(id).value='';
  const panel=document.querySelector('.machine-add');
  if(panel){panel.open=true;panel.scrollIntoView?.({behavior:'smooth',block:'start'})}
  renderMachineDuplicateWarning();
  message('Recovery settings copied from '+host.name+' ('+host.id+'). Supply the SSH address and user, verify its host key again, then enroll. Old records will not be deleted.','success');
  document.getElementById('machine-address').focus?.();
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

function chooseMachineMode(){
  const native=document.getElementById('machine-mode').value==='android-emulator';
  const name=document.getElementById('machine-name');
  const tags=document.getElementById('machine-tags');
  const privileged=document.getElementById('machine-allow-privileged');
  const button=document.getElementById('add-machine');
  const hint=document.getElementById('machine-mode-help');
  if(native){
    if(name.value.trim()&&!name.value.trim().endsWith('-android'))name.value=name.value.trim()+'-android';
    const existing=tags.value.split(',').map(s=>s.trim()).filter(Boolean);
    if(!existing.includes('android-emulator'))existing.push('android-emulator');
    tags.value=existing.join(', ');
    privileged.checked=false;
    privileged.disabled=true;
    button.textContent='Set up Android KVM agent';
    hint.textContent='Installs a dedicated unprivileged native agent and verified GitHub Actions runner on this machine, without touching its existing Docker agent. Requires Linux x64, KVM, Docker for binary extraction, and sudo during SSH setup. Only trusted private repositories should run here.';
  }else{
    privileged.disabled=false;
    button.textContent='Add machine';
    hint.textContent='SSH is used only for setup. If sudo needs a password, RunnerHub uses the sudo password above or falls back to the SSH password. Publisher support enables privileged per-job Docker-in-Docker sidecars, not host Docker socket access inside workflow jobs.';
  }
}

async function addMachine(){
  const button=document.getElementById('add-machine');
  const body={
    mode:document.getElementById('machine-mode').value,
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
  if(body.mode==='android-emulator'&&body.allowPrivilegedWorkers)return message('Native Android KVM agents cannot enable publisher/DinD privileges.','error');
  if(machineBootstrap?.state==='working')return message('A machine enrollment is already underway. Check its status before retrying.','error');
  button.disabled=true;
  const startedAt=new Date().toISOString();
  setMachineBootstrap('working',{name:body.name,startedAt});
  try{
    const op=await j('/api/v1/hosts/bootstrap-operations',{method:'POST',
      headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    if(!op.id)throw new Error('No enrollment operation ID returned by RunnerHub.');
    // Credentials should not remain in the form after the server has accepted them.
    for(const id of ['machine-password','machine-sudo-password','machine-private-key',
      'machine-registry-password'])document.getElementById(id).value='';
    machineOperation=op;
    setMachineBootstrap('working',{name:op.target||body.name,operationId:op.id,startedAt:op.startedAt});
    message(op.reused?'An enrollment for this machine name is already in progress. Showing the existing operation.':
      'Enrollment accepted; see the stage timeline in Hosts.','success');
    await refreshMachineOperation();
    await refreshRecentOperations();
  }catch(e){
    machineOperation=null;
    setMachineBootstrap('unknown',{name:body.name,startedAt});
    message('Enrollment request not confirmed: '+e.message+'. Verify the host and recent operations before retrying.','error');
  }finally{button.disabled=false}
}
// This action is offered only when GitHub reports completion while the
// RunnerHub worker still says busy. The server independently verifies both.
async function reconcileFinishedGitHubWorker(button){
  const workerID=button.dataset.workerReconcile;
  if(!workerID)return;
  const worker=(snapshot.workers||[]).find(w=>w.id===workerID);
  const progress=(snapshot.progress||[]).find(p=>p.workerId===workerID);
  const confirmed=githubCompletedWorkerMismatch(worker,progress);
  const needsURL=legacyBusyWorkerNeedsJobURL(worker);
  if(!confirmed&&!needsURL){
    return message('GitHub completion is not confirmed for this worker. Refresh before recovery.','error');
  }
  let githubJobUrl='';
  if(needsURL&&!confirmed){
    const entered=window.prompt('Paste the full completed GitHub job URL (https://github.com/owner/repo/actions/runs/123/job/456). RunnerHub will independently verify the assigned runner and terminal result.','');
    if(entered===null)return;
    githubJobUrl=entered.trim();
    if(!/^https:\/\/github\.com\/[^/]+\/[^/]+\/actions\/runs\/[0-9]+\/job\/[0-9]+$/.test(githubJobUrl))
      return message('A full canonical GitHub job URL is required.','error');
  }
  const consequence=(confirmed?'GitHub reports '+(progress.conclusion||'completion')+' for '+(worker.jobName||workerID)+'.':
    'RunnerHub will verify the supplied GitHub job and runner identity before changing any worker state.')+
    ' If completed, reconcile RunnerHub as terminal and queue provider cleanup? This may stop an orphaned container.';
  if(!window.confirm(consequence))return;
  button.disabled=true;
  try{
    const opts=githubJobUrl?{method:'POST',headers:{'content-type':'application/json'},
      body:JSON.stringify({githubJobUrl})}:{method:'POST'};
    const result=await j('/api/v1/workers/'+encodeURIComponent(workerID)+'/reconcile-github',opts);
    message(result.detail||'GitHub completion verified; worker reconciled.','success');
    await refresh(true);
  }catch(e){
    message('Reconciliation refused: '+e.message,'error');
  }finally{button.disabled=false}
}

async function archiveOldHost(button){
  const id=button.dataset.hostArchive;
  const host=(snapshot.hosts||[]).find(h=>String(h.id)===id);
  if(!host||String(host.state).toLowerCase()!=='offline'){
    return message('Only offline hosts can be archived. Refresh and retry.','error');
  }
  const merging=button.dataset.hostMerge==='true';
  const candidates=(snapshot.hosts||[]).filter(t=>t.id!==id&&String(t.state).toLowerCase()==='online'&&
    String(t.name||'').trim().toLowerCase()===String(host.name||'').trim().toLowerCase()&&t.provider===host.provider);
  let targetHostId='';
  if(merging){
    if(!candidates.length)return message('No matching online host. Refresh the list or archive without merging.','error');
    if(candidates.length===1)targetHostId=candidates[0].id;
    else{
      const choice=window.prompt('Enter the full ID of the destination machine ('+
        candidates.map(h=>h.name+': '+h.id).join(', ')+'). Only matching online hosts are allowed:','');
      if(!choice)return;
      targetHostId=choice.trim();
      if(!candidates.some(c=>c.id===targetHostId))return message('Destination host ID did not match an online candidate.','error');
    }
  }
  const explanation=merging?'Link its previous enrollment history to '+targetHostId+'. ':
    'Archive this old host identity. ';
  const answer=window.prompt(explanation+'The original agent identity will be retired and its historical job records preserved. Confirm you have verified the machines are the same (when merging). Type OLD host ID '+id+' to proceed:','');
  if(answer!==id)return;
  button.disabled=true;
  try{
    await j('/api/v1/hosts/'+encodeURIComponent(id)+'/archive',{
      method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({targetHostId})
    });
    message(merging?'Merged previous enrollment into '+targetHostId+'.':'Archived old host '+id+'.','success');
    await refresh(true);
  }catch(e){message('Host archival refused: '+e.message,'error')}
  finally{button.disabled=false}
}

async function toggleHostScheduling(button){
  const id=button.dataset.hostSchedulingToggle;
  const disabled=button.dataset.hostSchedulingDisabled==='true';
  button.disabled=true;
  try{
    await j('/api/v1/hosts/'+encodeURIComponent(id)+'/scheduling',{method:'PUT',headers:{'content-type':'application/json'},body:JSON.stringify({schedulingDisabled:disabled})});
    message(disabled?'Host disabled for new runner placement. Existing workers remain running.':'Host enabled for runner placement.','success');
    await refresh(true);
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function saveHostTags(button){
  const id=button.dataset.hostTags;
  const input=els.hosts.querySelector('[data-host-tag-input="'+CSS.escape(id)+'"]');
  const priorityInput=els.hosts.querySelector('[data-host-priority-input="'+CSS.escape(id)+'"]');
  const placementDomainInput=els.hosts.querySelector('[data-host-placement-domain-input="'+CSS.escape(id)+'"]');
  const priority=Number(priorityInput?.value||0);
  if(!Number.isInteger(priority)||priority < -1000||priority > 1000)return message('Host priority must be an integer between -1000 and 1000.','error');
  const tags={};
  (input?.value||'').split(',').map(x=>x.trim()).filter(Boolean).forEach(tag=>{tags[tag]='true'});
  button.disabled=true;
  try{
    await j('/api/v1/hosts/'+encodeURIComponent(id),{method:'PATCH',headers:{'content-type':'application/json'},body:JSON.stringify({tags,priority,placementDomainId:placementDomainInput?.value||''})});
    hostDrafts.delete(id);
    message('Machine scheduling settings updated.','success');
    await refresh(true);
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function resetPoolLearning(button){
  const poolId=button.dataset.poolId;
  if(!poolId)return;
  const label=button.dataset.learningLabel||'runner';
  button.disabled=true;
  try{
    await j('/api/v1/pools/'+encodeURIComponent(poolId)+'/learning/reset',{method:'POST'});
    message('Reset learning for '+label+'. Historical job records were kept; sizing will wait for new valid samples.','success');
    await refresh();
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function resetRepositoryLearning(button){
  const ids=String(button.dataset.repoLearningReset||'').split(',').map(x=>x.trim()).filter(Boolean);
  if(!ids.length)return;
  const repo=button.dataset.repoName||'repository';
  button.disabled=true;
  try{
    await Promise.all(ids.map(id=>j('/api/v1/pools/'+encodeURIComponent(id)+'/learning/reset',{method:'POST'})));
    message('Reset resource learning for all '+ids.length+' runner class'+(ids.length===1?'':'es')+' in '+repo+'. Historical jobs were kept.','success');
    await refresh();
  }catch(e){message(e.message,'error')}finally{button.disabled=false}
}

async function copyVercelWorkflow(button){
  try{
    await navigator.clipboard.writeText(vercelWorkflowTemplate);
    message('Vercel deployment workflow copied.','success');
  }catch{
    const panel=button.closest('.vercel-config');
    const area=panel?panel.querySelector('textarea'):null;
    if(area){area.focus();area.select()}
    message('Could not access the clipboard. The workflow is selected for manual copy.','error');
  }
}

async function saveVercelProfile(button){
  const enabled=button.dataset.vercelAction!=='disable';
  const body={installationId:Number(button.dataset.installation),repository:button.dataset.repo,name:VERCEL_PROFILE_NAME,enabled};
  if(enabled){
    body.tags=[VERCEL_MACHINE_TAG];
    body.isolation='container-trusted';
    body.maxConcurrency=1;
    body.resources={cpuMillis:2000,memoryBytes:4294967296,diskBytes:21474836480};
    body.autoTuneResources=false;
  }
  button.disabled=true;
  try{
    message((enabled?'Configuring ':'Disabling ')+button.dataset.repo+' Vercel deploy runner…');
    await j('/api/v1/github/profile',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    message(enabled?'Vercel deploy runner configured. Use runs-on: '+VERCEL_PROFILE_NAME+'.':'Vercel deploy runner disabled.','success');
    const panel=button.closest('.vercel-config');if(panel)panel.open=false;
    await refresh();
  }catch(e){message(e.message,'error');button.disabled=false}
}

function applyAndroidEmulatorPreset(button) {
  const panel=button.closest('.profile-config');
  if(!panel)return;
  const fields={
    name:'android-emulator',
    tags:'android-emulator, hardware.kvm',
    isolation:'host-persistent',
    cpu:'4',
    memory:'8',
    concurrency:'1',
  };
  for(const [key,value] of Object.entries(fields)){
    const el=panel.querySelector('[data-profile-field="'+key+'"]');
    if(el)el.value=value;
  }
  const auto=panel.querySelector('[data-profile-field="autotune"]');
  if(auto)auto.checked=false;
  message('Dedicated Android emulator profile filled. Connect a native KVM host before enabling it.','info');
}

function applyWindowsNativePreset(button) {
  const panel=button.closest('.profile-config');
  if(!panel)return;
  const fields={
    name:'windows',
    tags:'windows-msvc',
    isolation:'host-persistent',
    cpu:'4',
    memory:'16',
    concurrency:'1',
  };
  for(const [key,value] of Object.entries(fields)){
    const el=panel.querySelector('[data-profile-field="'+key+'"]');
    if(el)el.value=value;
  }
  const auto=panel.querySelector('[data-profile-field="autotune"]');
  if(auto)auto.checked=false;
  message('Windows MSVC profile filled. It targets system-reported Windows amd64 hosts and runs with the enrolled agent account.','info');
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


async function bulkConnectRepositories(){
  if(bulkConnecting||!lastRepoRenderArgs||!bulkSelected.size)return;
  const [repos,pools,, ,hosts]=lastRepoRenderArgs;
  const options={
    publisher:document.getElementById('bulk-publisher').checked,
    fast:document.getElementById('bulk-fast').checked
  };
  const plan=window.RunnerHubQueueDiagnostics.planOnboarding(
    [...bulkSelected],repos,pools,hosts,options);
  const output=document.getElementById('bulk-results');
  if(!plan.operations.length){
    output.textContent='Nothing to change. '+plan.skipped.map(x=>x.repository+': '+x.reason).join('; ');
    return;
  }
  const summary=plan.operations.map(o=>o.repository+' · '+o.kind).join('\\n');
  if(!window.confirm('Connect '+plan.operations.length+' repository-scoped runner pools?\\n\\n'+summary+
    '\\n\\nNo public or unselected repository will be changed.'))return;
  bulkConnecting=true;
  updateBulkCount();
  const outcomes=[];
  const failedRepos=new Set();
  try{
    for(const operation of plan.operations){
      const key=operation.repository.toLowerCase();
      if(failedRepos.has(key)){
        outcomes.push(operation.repository+' '+operation.kind+': skipped because an earlier operation failed.');
        continue;
      }
      output.textContent='Applying '+operation.repository+' · '+operation.kind+'…';
      try{
        await j(operation.endpoint,{method:'POST',headers:{'content-type':'application/json'},
          body:JSON.stringify(operation.payload)});
        outcomes.push(operation.repository+' '+operation.kind+': configuration accepted; listener readiness pending.');
      }catch(error){
        outcomes.push(operation.repository+' '+operation.kind+': FAILED — '+String(error?.message||error));
        failedRepos.add(key);
      }
    }
    for(const item of plan.skipped)outcomes.push(item.repository+': skipped — '+item.reason);
    for(const repo of bulkSelected){
      if(!failedRepos.has(repo)&&!plan.skipped.some(x=>x.repository.toLowerCase()===repo))bulkSelected.delete(repo);
    }
    output.textContent=outcomes.join('\\n')+
      '\\nRefresh the Diagnostics page for actual listener and host readiness. Configuration is not job acceptance.';
    message('Bulk onboarding finished. Check the per-repository results and listener status.',failedRepos.size?'error':'success');
  }finally{
    bulkConnecting=false;
    updateBulkCount();
    await refresh();
  }
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
    const disk=Number(panel.querySelector('[data-publisher-field="disk"]').value);
    const concurrency=Number(panel.querySelector('[data-publisher-field="concurrency"]').value);
    if(!Number.isFinite(cpu)||cpu<=0)return message('Publisher CPU must be greater than 0.','error');
    if(!Number.isFinite(memory)||memory<=0)return message('Publisher RAM must be greater than 0.','error');
    if(!Number.isFinite(disk)||disk<1||!Number.isSafeInteger(Math.round(disk*1073741824)))return message('Publisher disk must be at least 1 GiB and a valid number.','error');
    if(!Number.isInteger(concurrency)||concurrency<1)return message('Publisher concurrency must be at least 1.','error');
    body.resources={cpuMillis:Math.round(cpu*1000),memoryBytes:Math.round(memory*1073741824),diskBytes:Math.round(disk*1073741824)};
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

restoreHistoryFiltersFromURL();
initPageNav();
window.addEventListener('popstate',()=>{
  restoreHistoryFiltersFromURL();
  if(location.hash==='#activity'&&(auth.authenticated||els.token.value))loadHistory();
});
renderMachineBootstrap();
refresh();
refreshMachineOperation();
refreshRecentOperations();
setInterval(()=>{
  if(document.hidden)return;
  renderMachineBootstrap();
  refreshMachineOperation();
  if(els.autoRefresh?.checked){refreshRecentOperations();if(!refreshInFlight)refresh()}
},10000);
