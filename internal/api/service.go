package api

import (
	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/auth"
	cachepkg "github.com/eutelic/runnerhub/internal/cache"
	"github.com/eutelic/runnerhub/internal/cireconcile"
	"github.com/eutelic/runnerhub/internal/deployer"
	"github.com/eutelic/runnerhub/internal/githubauth"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/machinebootstrap"
	"github.com/eutelic/runnerhub/internal/mcpaccess"
	"github.com/eutelic/runnerhub/internal/policy"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
	"github.com/eutelic/runnerhub/internal/telemetry"
	"net/http"
	"sync"
)

type Service struct {
	Store                     store.Store
	GitHub                    githubrunner.Backend
	Catalog                   githubrunner.Catalog
	GitHubAuth                *githubauth.Manager
	Hub                       *agenthub.Hub
	Scheduler                 scheduler.Scheduler
	Policy                    policy.Engine
	Metrics                   *telemetry.Metrics
	EnrollmentToken           string
	Auth                      *auth.Manager
	MCP                       *mcpaccess.Registry
	Cache                     *cachepkg.Store
	Static                    http.Handler
	MachineBootstrap          machinebootstrap.Bootstrapper
	MachineBootstrapURL       string
	MachineAgentImage         string
	AgentUpdateTargetImage    string
	AgentUpdateTargetRevision string
	AgentUpdateHelperImage    string
	AgentUpdateEnabled        bool
	DeployerBootstrap         *deployer.BootstrapManager
	CIReconcilerStatus        *cireconcile.Status
	fleetAlertMu              sync.Mutex
	workerScheduleMu          sync.Mutex
	hostPortProbeMu           sync.Mutex
	hostPortProbeWaiters      map[string]chan agent.CommandResult
	poolMutationMu            sync.Mutex
	workflowJobsMu            sync.Mutex
	workflowJobsCache         map[string]workflowJobsCacheEntry
	workflowJobsPending       map[string]*workflowJobsFlight
	scaleSetStatusMu          sync.RWMutex
	scaleSetStatuses          map[string]scaleSetStatusResponse
}

func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/mcp/pair/status", s.mcpPairStatus)
	m.HandleFunc("POST /api/v1/mcp/pair/request", s.mcpPairRequest)
	m.HandleFunc("GET /mcp/connections", s.mcpConnections)
	m.HandleFunc("POST /mcp/connections", s.mcpConnections)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /auth/github/status", s.githubAuthStatus)
	m.HandleFunc("GET /auth/github/login", s.githubLogin)
	m.HandleFunc("GET /auth/github/callback", s.githubCallback)
	m.HandleFunc("GET /auth/github/install", s.githubInstall)
	m.HandleFunc("GET /auth/github/setup", s.githubSetup)
	m.HandleFunc("POST /auth/logout", s.githubLogout)
	m.HandleFunc("POST /api/v1/agent/enroll", s.enroll)
	m.HandleFunc("POST /api/v1/agent/heartbeat", s.heartbeat)
	m.HandleFunc("GET /api/v1/agent/commands", s.commands)
	m.HandleFunc("GET /api/v1/agent/update-status", s.agentUpdateStatus)
	m.HandleFunc("POST /api/v1/agent/update-result", s.agentUpdateResult)
	m.HandleFunc("POST /api/v1/agent/update-recovery-start", s.agentUpdateBeginRecovery)
	m.HandleFunc("POST /api/v1/agent/results", s.results)
	m.HandleFunc("GET /api/v1/maintenance", s.getMaintenance)
	m.HandleFunc("POST /api/v1/maintenance/pause", s.pauseMaintenance)
	m.HandleFunc("POST /api/v1/maintenance/resume", s.resumeMaintenance)
	m.HandleFunc("GET /api/v1/hosts", s.hosts)
	m.HandleFunc("POST /api/v1/hosts/ssh-fingerprints", s.scanSSHHostKey)
	m.HandleFunc("POST /api/v1/hosts/bootstrap", s.bootstrapHost)
	m.HandleFunc("POST /api/v1/hosts/bootstrap-operations", s.startHostBootstrapOperation)
	m.HandleFunc("GET /api/v1/operations", s.listHostOperations)
	m.HandleFunc("GET /api/v1/operations/{id}", s.getHostOperation)
	m.HandleFunc("GET /api/v1/hosts/{id}/agent-update-preflight", s.agentUpdatePreflight)
	m.HandleFunc("GET /api/v1/hosts/{id}/update-agent", s.hostAgentUpdateOperation)
	m.HandleFunc("POST /api/v1/hosts/{id}/update-agent", s.updateAgent)
	m.HandleFunc("POST /api/v1/hosts/{id}/drain", s.drain)
	m.HandleFunc("POST /api/v1/hosts/{id}/archive", s.archiveHost)
	m.HandleFunc("PATCH /api/v1/hosts/{id}", s.updateHost)
	m.HandleFunc("PUT /api/v1/hosts/{id}/scheduling", s.setHostScheduling)
	m.HandleFunc("POST /api/v1/hosts/{id}/port-preflight", s.hostPortPreflight)
	m.HandleFunc("POST /api/v1/hosts/drain", s.bulkDrain)
	m.HandleFunc("GET /api/v1/pools", s.pools)
	m.HandleFunc("POST /api/v1/pools", s.createPool)
	m.HandleFunc("PATCH /api/v1/pools/{id}/resources", s.updatePoolResources)
	m.HandleFunc("PUT /api/v1/pools/{id}/image", s.updatePoolImage)
	m.HandleFunc("GET /api/v1/pools/{id}/health", s.poolHealth)
	m.HandleFunc("GET /api/v1/pools/health", s.poolHealthAll)
	m.HandleFunc("GET /api/v1/runtime-health", s.runtimeHealth)
	m.HandleFunc("GET /api/v1/fleet-alerts", s.fleetAlerts)
	m.HandleFunc("POST /api/v1/fleet-alerts/{id}/{action}", s.updateFleetAlert)
	m.HandleFunc("GET /api/v1/pools/{id}/recommendation", s.poolRecommendation)
	m.HandleFunc("POST /api/v1/pools/{id}/learning/reset", s.resetPoolLearning)
	m.HandleFunc("GET /api/v1/pools/{id}/security", s.poolSecurity)
	m.HandleFunc("GET /api/v1/workers", s.workers)
	m.HandleFunc("GET /api/v1/workers/history", s.workerHistory)
	m.HandleFunc("GET /api/v1/repositories/{owner}/{repo}/ci-efficiency", s.ciEfficiency)
	m.HandleFunc("GET /api/v1/workers/progress", s.workerProgress)
	m.HandleFunc("POST /api/v1/workers/{id}/reconcile-github", s.reconcileGitHubCompletedWorker)
	m.HandleFunc("POST /api/v1/pools/{id}/workers", s.createWorker)
	m.HandleFunc("POST /api/v1/pools/{id}/scale-workers", s.createScaleWorker)
	m.HandleFunc("POST /api/v1/scale-events", s.scaleEvent)
	m.HandleFunc("GET /api/v1/audit", s.audit)
	m.HandleFunc("GET /api/v1/metrics", s.metrics)
	m.HandleFunc("GET /api/v1/fleet/versions", s.versions)
	m.HandleFunc("POST /api/v1/cost/estimate", s.costEstimate)
	m.HandleFunc("PUT /api/v1/cache", s.cachePut)
	m.HandleFunc("GET /api/v1/cache/{key}", s.cacheGet)
	m.HandleFunc("DELETE /api/v1/cache/{key}", s.cacheDelete)
	m.HandleFunc("GET /api/v1/github/repositories", s.repositories)
	m.HandleFunc("GET /api/v1/github/installations", s.githubInstallations)
	m.HandleFunc("GET /api/v1/github/connected-repositories", s.connectedRepositories)
	m.HandleFunc("GET /api/v1/github/repositories/{owner}/{repo}/queued-jobs", s.queuedGitHubJobs)
	m.HandleFunc("POST /api/v1/github/connect", s.connectRepository)
	m.HandleFunc("POST /api/v1/github/organization-pools", s.configureOrganizationPool)
	m.HandleFunc("POST /api/v1/github/profile", s.configureRunnerProfile)
	m.HandleFunc("POST /api/v1/github/publisher", s.configurePublisherRepository)
	m.HandleFunc("GET /api/v1/github/runner-groups", s.runnerGroups)
	m.HandleFunc("GET /api/v1/scalesets/configs", s.scaleSetConfigs)
	m.HandleFunc("GET /api/v1/scalesets/status", s.scaleSetStatus)
	m.HandleFunc("POST /api/v1/scalesets/status", s.scaleSetStatus)
	m.HandleFunc("GET /api/v1/deployer/status", s.deployerStatus)
	m.HandleFunc("POST /api/v1/deployer/install", s.installDeployer)
	m.HandleFunc("GET /api/v1/deployer/apps", s.deployerApps)
	m.HandleFunc("POST /api/v1/deployer/apps/{app}/enabled", s.setDeployerApp)
	m.HandleFunc("POST /api/v1/deployer/apps/{app}/redeploy", s.redeployApp)
	m.HandleFunc("POST /api/v1/deployer/apps/{app}/automatic", s.configureAutoPublishing)
	if s.Static != nil {
		m.Handle("/", s.Static)
	}
	h := securityHeaders(m)
	if s.Auth != nil {
		h = s.Auth.Middleware(h)
	}
	return h
}
