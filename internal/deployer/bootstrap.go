package deployer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBootstrapRoot = "/data/deployer-bootstrap"
	defaultDeployerApp   = "runnerhub-deployer"
	defaultTargetID      = "runnerhub"
	defaultDeployPort    = 8790
)

var appNamePattern = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

type TrueNASBootstrapClient interface {
	EnsureCustomApp(context.Context, string, string) error
	FindAppMount(context.Context, string, string) (string, error)
}

type BootstrapClientFactory func(TrueNASConfig) (TrueNASBootstrapClient, error)

type BootstrapManager struct {
	autoMu    sync.Mutex
	MaintenanceToken string
	Root      string
	NewClient BootstrapClientFactory
	HTTP      *http.Client
	RegistryDigests ImageDigestResolver
}

type BootstrapRequest struct {
	Endpoint           string `json:"endpoint"`
	Port               int    `json:"port,omitempty"`
	Username           string `json:"username"`
	APIKey             string `json:"apiKey"`
	HostDataPath       string `json:"hostDataPath"`
	RunnerHubApp       string `json:"runnerHubApp"`
	HealthURL          string `json:"healthUrl"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

type BootstrapResult struct {
	State              string `json:"state"`
	DeployerApp        string `json:"deployerApp"`
	DeployerURL        string `json:"deployerUrl"`
	TargetID           string `json:"targetId"`
	WorkflowToken      string `json:"workflowToken,omitempty"`
	WorkflowSecretName string `json:"workflowSecretName,omitempty"`
}

type BootstrapStatus struct {
	Available    bool      `json:"available"`
	Configured   bool      `json:"configured"`
	Healthy      bool      `json:"healthy"`
	DeployerApp  string    `json:"deployerApp,omitempty"`
	DeployerURL  string    `json:"deployerUrl,omitempty"`
	TargetID     string    `json:"targetId,omitempty"`
	ConfiguredAt time.Time `json:"configuredAt,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

type bootstrapState struct {
	DeployerApp  string    `json:"deployerApp"`
	DeployerURL  string    `json:"deployerUrl"`
	TargetID     string    `json:"targetId"`
	ConfiguredAt time.Time `json:"configuredAt"`
}

type bootstrapFileSnapshot struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func snapshotBootstrapFile(path string) (bootstrapFileSnapshot, error) {
	snapshot := bootstrapFileSnapshot{path: path}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, fmt.Errorf("inspect existing deployer bootstrap file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return snapshot, fmt.Errorf("existing deployer bootstrap path is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot, fmt.Errorf("read existing deployer bootstrap file: %w", err)
	}
	snapshot.data = data
	snapshot.mode = info.Mode().Perm()
	snapshot.exists = true
	return snapshot, nil
}

func restoreBootstrapFiles(snapshots []bootstrapFileSnapshot) error {
	var restoreErr error
	for _, snapshot := range snapshots {
		if snapshot.exists {
			if err := writePrivateFile(snapshot.path, snapshot.data); err != nil {
				restoreErr = errors.Join(restoreErr, err)
				continue
			}
			if err := os.Chmod(snapshot.path, snapshot.mode); err != nil {
				restoreErr = errors.Join(restoreErr, err)
			}
			continue
		}
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return restoreErr
}

func (m *BootstrapManager) Install(ctx context.Context, req BootstrapRequest) (BootstrapResult, error) {
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Username = strings.TrimSpace(req.Username)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.HostDataPath = strings.TrimSpace(req.HostDataPath)
	req.RunnerHubApp = strings.TrimSpace(req.RunnerHubApp)
	req.HealthURL = strings.TrimSpace(req.HealthURL)
	if req.RunnerHubApp == "" {
		req.RunnerHubApp = "runnerhub"
	}
	if req.Endpoint == "" || req.Username == "" || req.APIKey == "" || req.HealthURL == "" {
		return BootstrapResult{}, errors.New("TrueNAS URL, username, API key, and health URL are required")
	}
	if !appNamePattern.MatchString(req.RunnerHubApp) || len(req.RunnerHubApp) > 40 {
		return BootstrapResult{}, errors.New("RunnerHub TrueNAS app name is invalid")
	}
	if req.Port == 0 {
		req.Port = defaultDeployPort
	}
	if req.Port < 1024 || req.Port > 65535 {
		return BootstrapResult{}, errors.New("redeployer port must be between 1024 and 65535")
	}
	endpoint, err := url.Parse(req.Endpoint)
	if err != nil || endpoint.Scheme != "wss" || endpoint.Host == "" {
		return BootstrapResult{}, errors.New("TrueNAS URL must be an absolute wss:// URL")
	}
	health, err := url.Parse(req.HealthURL)
	if err != nil || (health.Scheme != "http" && health.Scheme != "https") || health.Host == "" || health.User != nil {
		return BootstrapResult{}, errors.New("health URL must be an absolute http(s) URL without credentials")
	}
	factory := m.NewClient
	if factory == nil {
		factory = func(cfg TrueNASConfig) (TrueNASBootstrapClient, error) {
			return NewTrueNASClient(cfg)
		}
	}
	client, err := factory(TrueNASConfig{
		Endpoint:           req.Endpoint,
		Username:           req.Username,
		APIKey:             req.APIKey,
		InsecureSkipVerify: req.InsecureSkipVerify,
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	// Self-redeployment needs a trusted admin credential for the maintenance
	// gate. The workflow token is NEVER granted this authority.
	maintenanceToken:=strings.TrimSpace(m.MaintenanceToken)
	if maintenanceToken==""{
		return BootstrapResult{},errors.New("RunnerHub self-redeployment requires the control-plane admin key; install from a configured trusted RunnerHub control instance")
	}
	if health.Path!="/healthz" || health.RawQuery!="" || health.Fragment!="" {
		return BootstrapResult{},errors.New("RunnerHub health URL must end in /healthz for coordinated self-redeployment")
	}
	maintenanceEndpoint:=*health
	maintenanceEndpoint.Path=""
	maintenanceEndpoint.RawPath=""
	maintenanceEndpoint.RawQuery=""
	maintenanceEndpoint.Fragment=""
	if req.HostDataPath == "" {
		req.HostDataPath, err = client.FindAppMount(ctx, req.RunnerHubApp, "/data")
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("auto-detect RunnerHub TrueNAS data path: %w", err)
		}
	}
	if err := validateHostDataPath(req.HostDataPath); err != nil {
		return BootstrapResult{}, err
	}

	root := strings.TrimSpace(m.Root)
	if root == "" {
		root = defaultBootstrapRoot
	}
	if !filepath.IsAbs(root) {
		return BootstrapResult{}, errors.New("deployer bootstrap root must be absolute")
	}
	hostRoot := filepath.Join(req.HostDataPath, "deployer-bootstrap")
	configDir := filepath.Join(root, "config")
	secretsDir := filepath.Join(root, "secrets")
	dataDir := filepath.Join(root, "data")
	for _, dir := range []string{configDir, secretsDir, dataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return BootstrapResult{}, fmt.Errorf("create deployer bootstrap directory: %w", err)
		}
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return BootstrapResult{}, fmt.Errorf("generate deployment token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	sum := sha256.Sum256([]byte(token))
	policy := Policy{Targets: []Target{{
		ID:             defaultTargetID,
		Repository:     "eutelic/runnerhub",
		App:            req.RunnerHubApp,
		Image:          "ghcr.io/eutelic/runnerhub-control:latest",
		TokenSHA256:    hex.EncodeToString(sum[:]),
		HealthURL:      req.HealthURL,
		TimeoutSeconds: 180,
		SelfRedeploy:true,
		MaintenanceURL:maintenanceEndpoint.String(),
		DrainTimeoutSeconds:14400,
	}}}
	policyBytes, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("encode deployment policy: %w", err)
	}
	policyBytes = append(policyBytes, '\n')

	deployerURL := deployerHTTPURL(endpoint, req.Port)
	state := bootstrapState{
		DeployerApp:  defaultDeployerApp,
		DeployerURL:  deployerURL,
		TargetID:     defaultTargetID,
		ConfiguredAt: time.Now().UTC(),
	}
	stateBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("encode deployment bootstrap state: %w", err)
	}
	stateBytes = append(stateBytes, '\n')

	keyPath := filepath.Join(secretsDir, "truenas-api-key")
	maintenanceKeyPath := filepath.Join(secretsDir, "runnerhub-maintenance-key")
	policyPath := filepath.Join(configDir, "deployments.json")
	statePath := filepath.Join(root, "state.json")
	paths := []string{keyPath, maintenanceKeyPath, policyPath, statePath}
	snapshots := make([]bootstrapFileSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot, err := snapshotBootstrapFile(path)
		if err != nil {
			return BootstrapResult{}, err
		}
		snapshots = append(snapshots, snapshot)
	}
	rollback := func(cause error) (BootstrapResult, error) {
		if restoreErr := restoreBootstrapFiles(snapshots); restoreErr != nil {
			return BootstrapResult{}, fmt.Errorf("%w; restore previous deployer bootstrap files: %v", cause, restoreErr)
		}
		return BootstrapResult{}, cause
	}
	if err := writePrivateFile(keyPath, []byte(req.APIKey+"\n")); err != nil {
		return rollback(err)
	}
	if err:=writePrivateFile(maintenanceKeyPath,[]byte(maintenanceToken+"\n"));err!=nil{return rollback(err)}
	if err := writePrivateFile(policyPath, policyBytes); err != nil {
		return rollback(err)
	}
	if err := writePrivateFile(statePath, stateBytes); err != nil {
		return rollback(err)
	}

	compose := bootstrapCompose(hostRoot, req.Endpoint, req.Username, req.InsecureSkipVerify, req.Port)
	if err := client.EnsureCustomApp(ctx, defaultDeployerApp, compose); err != nil {
		if errors.Is(err, ErrAppInstallMayBeInProgress) {
			// TrueNAS can still be creating the app asynchronously. The new
			// Custom App may mount this API-key path after this request returns;
			// removing the file here would cause a second, misleading failure.
			// Retain the private key, policy and bootstrap state for a later retry.
			return BootstrapResult{}, fmt.Errorf("install standalone deployer: %w (bootstrap files retained while TrueNAS may still be starting)", err)
		}
		return rollback(fmt.Errorf("install standalone deployer: %w", err))
	}

	return BootstrapResult{
		State:              "configured",
		DeployerApp:        defaultDeployerApp,
		DeployerURL:        deployerURL,
		TargetID:           defaultTargetID,
		WorkflowToken:      token,
		WorkflowSecretName: "RUNNERHUB_DEPLOY_TOKEN",
	}, nil
}

func (m *BootstrapManager) Status(ctx context.Context) BootstrapStatus {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		root = defaultBootstrapRoot
	}
	status := BootstrapStatus{Available: true}
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			status.Detail = "deployment bootstrap state is unreadable"
		}
		return status
	}
	var state bootstrapState
	if err := json.Unmarshal(data, &state); err != nil {
		status.Detail = "deployment bootstrap state is invalid"
		return status
	}
	status.Configured = true
	status.DeployerApp = state.DeployerApp
	status.DeployerURL = state.DeployerURL
	status.TargetID = state.TargetID
	status.ConfiguredAt = state.ConfiguredAt

	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(state.DeployerURL, "/")+"/healthz", nil)
	if err != nil {
		status.Detail = "invalid deployer health URL"
		return status
	}
	resp, err := client.Do(req)
	if err != nil {
		status.Detail = "standalone deployer is configured but unreachable"
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		status.Healthy = true
		status.Detail = "standalone deployer is healthy"
	} else {
		status.Detail = fmt.Sprintf("standalone deployer health returned HTTP %d", resp.StatusCode)
	}
	return status
}

func validateHostDataPath(path string) error {
	if !strings.HasPrefix(path, "/mnt/") || strings.ContainsAny(path, "\r\n\t") {
		return errors.New("RunnerHub data path must be an absolute TrueNAS /mnt path")
	}
	clean := filepath.Clean(path)
	if clean != path || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
		return errors.New("RunnerHub data path must be normalized and must not contain parent traversal")
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/mnt/"), "/") {
		if part == "" {
			return errors.New("RunnerHub data path contains an empty path component")
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
				continue
			}
			return errors.New("RunnerHub data path contains unsupported characters")
		}
	}
	return nil
}

func deployerHTTPURL(endpoint *url.URL, port int) string {
	return "http://" + net.JoinHostPort(endpoint.Hostname(), strconv.Itoa(port))
}

func bootstrapCompose(hostRoot, endpoint, username string, insecure bool, port int) string {
	q := func(v string) string { return strconv.Quote(v) }
	return fmt.Sprintf(`services:
  init-data:
    image: ghcr.io/eutelic/runnerhub-deployer:latest
    pull_policy: always
    user: "0:0"
    entrypoint: ["/bin/sh", "-ec"]
    command: ["mkdir -p /data && chown 10001:10001 /data"]
    restart: "no"
    volumes:
      - type: bind
        source: %s
        target: /data

  deployer:
    image: ghcr.io/eutelic/runnerhub-deployer:latest
    pull_policy: always
    restart: unless-stopped
    depends_on:
      init-data:
        condition: service_completed_successfully
    ports:
      - target: 8080
        published: %s
        protocol: tcp
    environment:
      RUNNERHUB_DEPLOY_HTTP_ADDR: 0.0.0.0:8080
      RUNNERHUB_DEPLOY_POLICY_FILE: /config/deployments.json
      RUNNERHUB_DEPLOY_AUDIT_FILE: /data/deployments.jsonl
      RUNNERHUB_TRUENAS_URL: %s
      RUNNERHUB_TRUENAS_USERNAME: %s
      RUNNERHUB_TRUENAS_API_KEY_FILE: /run/secrets/truenas-api-key
      RUNNERHUB_MAINTENANCE_TOKEN_FILE: /run/secrets/runnerhub-maintenance-key
      RUNNERHUB_TRUENAS_TLS_INSECURE_SKIP_VERIFY: %s
    secrets:
      - truenas-api-key
      - runnerhub-maintenance-key
    volumes:
      - type: bind
        source: %s
        target: /data
      - type: bind
        source: %s
        target: /config/deployments.json
        read_only: true
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:8080/healthz"]
      interval: 10s
      timeout: 3s
      retries: 12
      start_period: 5s

secrets:
  truenas-api-key:
    file: %s
  runnerhub-maintenance-key:
    file: %s
`,
		q(filepath.Join(hostRoot, "data")),
		q(strconv.Itoa(port)),
		q(endpoint),
		q(username),
		q(strconv.FormatBool(insecure)),
		q(filepath.Join(hostRoot, "data")),
		q(filepath.Join(hostRoot, "config", "deployments.json")),
		q(filepath.Join(hostRoot, "secrets", "truenas-api-key")),
		q(filepath.Join(hostRoot, "secrets", "runnerhub-maintenance-key")),
	)
}

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".runnerhub-deployer-*")
	if err != nil {
		return fmt.Errorf("create deployer bootstrap file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("persist deployer bootstrap file: %w", err)
	}
	return nil
}
