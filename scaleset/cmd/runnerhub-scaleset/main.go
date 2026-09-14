package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
)

type config struct {
	RegistrationURL   string
	Name              string
	Labels            []string
	RunnerGroup       string
	PoolID            string
	ControlURL        string
	ControlToken      string
	MinRunners        int
	MaxRunners        int
	AppClientID       string
	AppInstallationID int64
	AppPrivateKey     string
	PAT               string
	AllowInsecureHTTP bool
}

func loadConfig() (config, error) {
	c := config{
		RegistrationURL: os.Getenv("RUNNERHUB_GITHUB_URL"),
		Name:            os.Getenv("RUNNERHUB_SCALESET_NAME"),
		PoolID:          os.Getenv("RUNNERHUB_POOL_ID"),
		ControlURL:      strings.TrimRight(os.Getenv("RUNNERHUB_CONTROL_URL"), "/"),
		ControlToken:    os.Getenv("RUNNERHUB_CONTROL_TOKEN"),
		RunnerGroup:     os.Getenv("RUNNERHUB_RUNNER_GROUP"),
		PAT:             os.Getenv("RUNNERHUB_GITHUB_TOKEN"),
		AppClientID:     os.Getenv("RUNNERHUB_GITHUB_APP_CLIENT_ID"),
	}
	c.Labels = splitCSV(os.Getenv("RUNNERHUB_SCALESET_LABELS"))

	var err error
	if c.MinRunners, err = intEnv("RUNNERHUB_MIN_RUNNERS", 0); err != nil {
		return c, err
	}
	if c.MaxRunners, err = intEnv("RUNNERHUB_MAX_RUNNERS", 10); err != nil {
		return c, err
	}
	installation, err := intEnv("RUNNERHUB_GITHUB_APP_INSTALLATION_ID", 0)
	if err != nil {
		return c, err
	}
	c.AppInstallationID = int64(installation)
	if c.AllowInsecureHTTP, err = boolEnv("RUNNERHUB_ALLOW_INSECURE_HTTP", false); err != nil {
		return c, err
	}
	if path := os.Getenv("RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("read github app key: %w", err)
		}
		c.AppPrivateKey = string(b)
	}
	if c.RunnerGroup == "" {
		c.RunnerGroup = scaleset.DefaultRunnerGroup
	}
	if c.RegistrationURL == "" || c.Name == "" || c.PoolID == "" || c.ControlURL == "" {
		return c, errors.New("RUNNERHUB_GITHUB_URL, RUNNERHUB_SCALESET_NAME, RUNNERHUB_POOL_ID and RUNNERHUB_CONTROL_URL are required")
	}
	if c.MaxRunners < 1 || c.MinRunners < 0 || c.MinRunners > c.MaxRunners {
		return c, errors.New("invalid min/max runner values")
	}
	if err := requireProtectedURL(c.ControlURL, c.AllowInsecureHTTP, "RunnerHub control"); err != nil {
		return c, err
	}
	if err := requireProtectedURL(c.RegistrationURL, c.AllowInsecureHTTP, "GitHub registration"); err != nil {
		return c, err
	}
	if c.ControlToken == "" && !loopbackURL(c.ControlURL) {
		return c, errors.New("RUNNERHUB_CONTROL_TOKEN is required for a non-loopback control URL")
	}
	return c, nil
}

func (c config) client() (*scaleset.Client, error) {
	info := scaleset.SystemInfo{System: "runnerhub", Subsystem: "scaleset", Version: "0.3.0", CommitSHA: "unknown"}
	app := scaleset.GitHubAppAuth{ClientID: c.AppClientID, InstallationID: c.AppInstallationID, PrivateKey: c.AppPrivateKey}
	if app.Validate() == nil {
		return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
			GitHubConfigURL: c.RegistrationURL,
			GitHubAppAuth:   app,
			SystemInfo:      info,
		})
	}
	if c.PAT == "" {
		return nil, errors.New("github app credentials or RUNNERHUB_GITHUB_TOKEN are required")
	}
	return scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     c.RegistrationURL,
		PersonalAccessToken: c.PAT,
		SystemInfo:          info,
	})
}

type runnerHubClient struct {
	base   string
	token  string
	poolID string
	http   *http.Client
}

type workerResponse struct {
	ID         string `json:"id"`
	PoolID     string `json:"poolId"`
	RunnerName string `json:"runnerName"`
	State      string `json:"state"`
}

type poolResponse struct {
	ID             string `json:"id"`
	MaxConcurrency int    `json:"maxConcurrency"`
}

type scaleEvent struct {
	PoolID        string    `json:"poolId"`
	RunnerName    string    `json:"runnerName"`
	State         string    `json:"state"`
	Detail        string    `json:"detail,omitempty"`
	Repository    string    `json:"repository,omitempty"`
	WorkflowRef   string    `json:"workflowRef,omitempty"`
	WorkflowRunID int64     `json:"workflowRunId,omitempty"`
	JobID         string    `json:"jobId,omitempty"`
	JobName       string    `json:"jobName,omitempty"`
	QueuedAt      time.Time `json:"queuedAt,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	CompletedAt   time.Time `json:"completedAt,omitempty"`
	Result        string    `json:"result,omitempty"`
}

func (c runnerHubClient) create(ctx context.Context, name, jit string) (workerResponse, error) {
	var out workerResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/pools/"+c.poolID+"/scale-workers", map[string]string{
		"runnerName": name,
		"jitConfig":  jit,
	}, &out)
	return out, err
}

func (c runnerHubClient) event(ctx context.Context, e scaleEvent) error {
	e.PoolID = c.poolID
	return c.do(ctx, http.MethodPost, "/api/v1/scale-events", e, nil)
}

func (c runnerHubClient) activeWorkers(ctx context.Context) (map[string]string, error) {
	var workers []workerResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/workers", nil, &workers); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, w := range workers {
		if w.PoolID == c.poolID && w.RunnerName != "" && activeState(w.State) {
			out[w.RunnerName] = w.ID
		}
	}
	return out, nil
}

func (c runnerHubClient) poolLimit(ctx context.Context) (int, error) {
	var pools []poolResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/pools", nil, &pools); err != nil {
		return 0, err
	}
	for _, p := range pools {
		if p.ID == c.poolID {
			return p.MaxConcurrency, nil
		}
	}
	return 0, fmt.Errorf("runnerhub pool %q not found", c.poolID)
}

func (c runnerHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("runnerhub %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type scaler struct {
	mu         sync.Mutex
	names      map[string]string
	min        int
	max        int
	scaleSetID int
	github     *scaleset.Client
	hub        runnerHubClient
	logger     *slog.Logger
}

func (s *scaler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.names)
}

func (s *scaler) syncActiveWorkers(ctx context.Context) error {
	names, err := s.hub.activeWorkers(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.names = names
	s.mu.Unlock()
	return nil
}

func (s *scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	// RunnerHub independently reconciles failed/disappeared provider workers. Re-read
	// active state on every desired-count delivery so a runner that died before job
	// assignment cannot remain stuck in this listener's local capacity count.
	if err := s.syncActiveWorkers(ctx); err != nil {
		return s.count(), fmt.Errorf("refresh RunnerHub active workers: %w", err)
	}

	target := min(s.max, s.min+count)
	for s.count() < target {
		name := fmt.Sprintf("runnerhub-%d-%d", time.Now().UnixNano(), s.count())
		jit, err := s.github.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name}, s.scaleSetID)
		if err != nil {
			return s.count(), fmt.Errorf("generate jit: %w", err)
		}
		wr, err := s.hub.create(ctx, name, jit.EncodedJITConfig)
		if err != nil {
			return s.count(), err
		}
		s.mu.Lock()
		s.names[name] = wr.ID
		s.mu.Unlock()
		s.logger.Info("provisioning runner", "runner", name, "worker", wr.ID)
	}
	return s.count(), nil
}

func (s *scaler) HandleJobStarted(ctx context.Context, info *scaleset.JobStarted) error {
	started := time.Now().UTC()
	return s.hub.event(ctx, scaleEvent{
		RunnerName:    info.RunnerName,
		State:         "busy",
		Repository:    repoName(info.OwnerName, info.RepositoryName),
		WorkflowRef:   info.JobWorkflowRef,
		WorkflowRunID: info.WorkflowRunID,
		JobID:         info.JobID,
		JobName:       info.JobDisplayName,
		QueuedAt:      info.QueueTime,
		StartedAt:     started,
	})
}

func (s *scaler) HandleJobCompleted(ctx context.Context, info *scaleset.JobCompleted) error {
	completed := info.FinishTime
	if completed.IsZero() {
		completed = time.Now().UTC()
	}
	if err := s.hub.event(ctx, scaleEvent{
		RunnerName:    info.RunnerName,
		State:         "completed",
		Repository:    repoName(info.OwnerName, info.RepositoryName),
		WorkflowRef:   info.JobWorkflowRef,
		WorkflowRunID: info.WorkflowRunID,
		JobID:         info.JobID,
		JobName:       info.JobDisplayName,
		QueuedAt:      info.QueueTime,
		CompletedAt:   completed,
		Result:        info.Result,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.names, info.RunnerName)
	s.mu.Unlock()
	return nil
}

type managedConfig struct {
	RegistrationURL   string   `json:"registrationUrl"`
	Name              string   `json:"name"`
	Labels            []string `json:"labels,omitempty"`
	RunnerGroup       string   `json:"runnerGroup,omitempty"`
	PoolID            string   `json:"poolId"`
	MinRunners        int      `json:"minRunners"`
	MaxRunners        int      `json:"maxRunners"`
	AppInstallationID int64    `json:"appInstallationId"`
}

type managedListener struct {
	cancel      context.CancelFunc
	fingerprint string
}

type listenerResult struct {
	key         string
	fingerprint string
	err         error
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	dynamic, err := boolEnv("RUNNERHUB_DYNAMIC_CONFIG", false)
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(2)
	}
	if dynamic {
		base, err := loadManagerConfig()
		if err != nil {
			slog.Error("configuration", "error", err)
			os.Exit(2)
		}
		if err := runDynamicManager(ctx, base); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scale-set manager stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	c, err := loadConfig()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(2)
	}
	if err := runListener(ctx, c); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("listener stopped", "error", err)
		os.Exit(1)
	}
}

func loadManagerConfig() (config, error) {
	c := config{
		ControlURL:   strings.TrimRight(os.Getenv("RUNNERHUB_CONTROL_URL"), "/"),
		ControlToken: os.Getenv("RUNNERHUB_CONTROL_TOKEN"),
		PAT:          os.Getenv("RUNNERHUB_GITHUB_TOKEN"),
		AppClientID:  os.Getenv("RUNNERHUB_GITHUB_APP_CLIENT_ID"),
	}
	var err error
	if c.AllowInsecureHTTP, err = boolEnv("RUNNERHUB_ALLOW_INSECURE_HTTP", false); err != nil {
		return c, err
	}
	if path := os.Getenv("RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("read github app key: %w", err)
		}
		c.AppPrivateKey = string(b)
	}
	if c.ControlURL == "" {
		return c, errors.New("RUNNERHUB_CONTROL_URL is required in dynamic mode")
	}
	if err := requireProtectedURL(c.ControlURL, c.AllowInsecureHTTP, "RunnerHub control"); err != nil {
		return c, err
	}
	if c.ControlToken == "" && !loopbackURL(c.ControlURL) {
		return c, errors.New("RUNNERHUB_CONTROL_TOKEN is required for a non-loopback control URL")
	}
	app := scaleset.GitHubAppAuth{ClientID: c.AppClientID, InstallationID: 1, PrivateKey: c.AppPrivateKey}
	if app.Validate() != nil && c.PAT == "" {
		return c, errors.New("github app client id/private key or RUNNERHUB_GITHUB_TOKEN are required")
	}
	return c, nil
}

func runDynamicManager(ctx context.Context, base config) error {
	hub := runnerHubClient{
		base:  base.ControlURL,
		token: base.ControlToken,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
	active := map[string]managedListener{}
	results := make(chan listenerResult, 32)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	ready := false

	reconcile := func() {
		var remote []managedConfig
		if err := hub.do(ctx, http.MethodGet, "/api/v1/scalesets/configs", nil, &remote); err != nil {
			slog.Error("load dynamic scale-set configs", "error", err)
			return
		}
		desired := map[string]managedConfig{}
		for _, item := range remote {
			if item.PoolID == "" || item.RegistrationURL == "" || item.Name == "" || item.AppInstallationID <= 0 {
				slog.Error("ignoring invalid dynamic scale-set config", "pool", item.PoolID, "registrationURL", item.RegistrationURL)
				continue
			}
			desired[item.PoolID] = item
		}
		for key, running := range active {
			item, ok := desired[key]
			if !ok || configFingerprint(item) != running.fingerprint {
				running.cancel()
				delete(active, key)
			}
		}
		for key, item := range desired {
			if _, ok := active[key]; ok {
				continue
			}
			c := configFromManaged(base, item)
			childCtx, childCancel := context.WithCancel(ctx)
			fingerprint := configFingerprint(item)
			active[key] = managedListener{cancel: childCancel, fingerprint: fingerprint}
			go func(key, fingerprint string, cfg config) {
				err := runListener(childCtx, cfg)
				results <- listenerResult{key: key, fingerprint: fingerprint, err: err}
			}(key, fingerprint, c)
		}
		if !ready {
			ready = true
			slog.Info("scale-set manager ready", "listeners", len(active))
		}
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			for _, running := range active {
				running.cancel()
			}
			return ctx.Err()
		case <-ticker.C:
			reconcile()
		case result := <-results:
			running, ok := active[result.key]
			if ok && running.fingerprint == result.fingerprint {
				delete(active, result.key)
			}
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				slog.Error("managed scale-set listener stopped", "pool", result.key, "error", result.err)
			}
		}
	}
}

func configFingerprint(c managedConfig) string {
	b, _ := json.Marshal(c)
	return string(b)
}

func configFromManaged(base config, remote managedConfig) config {
	c := base
	c.RegistrationURL = remote.RegistrationURL
	c.Name = remote.Name
	c.Labels = remote.Labels
	c.RunnerGroup = remote.RunnerGroup
	c.PoolID = remote.PoolID
	c.MinRunners = remote.MinRunners
	c.MaxRunners = remote.MaxRunners
	c.AppInstallationID = remote.AppInstallationID
	if c.RunnerGroup == "" {
		c.RunnerGroup = scaleset.DefaultRunnerGroup
	}
	return c
}

func runListener(ctx context.Context, c config) error {
	if c.MaxRunners < 1 || c.MinRunners < 0 || c.MinRunners > c.MaxRunners {
		return errors.New("invalid min/max runner values")
	}
	if err := requireProtectedURL(c.RegistrationURL, c.AllowInsecureHTTP, "GitHub registration"); err != nil {
		return err
	}
	gh, err := c.client()
	if err != nil {
		return fmt.Errorf("github client: %w", err)
	}

	groupID := 1
	if c.RunnerGroup != scaleset.DefaultRunnerGroup {
		g, err := gh.GetRunnerGroupByName(ctx, c.RunnerGroup)
		if err != nil {
			return fmt.Errorf("runner group: %w", err)
		}
		groupID = g.ID
	}

	labels := make([]scaleset.Label, 0, len(c.Labels))
	for _, l := range c.Labels {
		labels = append(labels, scaleset.Label{Name: l})
	}
	if len(labels) == 0 {
		labels = []scaleset.Label{{Name: c.Name}}
	}

	set, err := gh.GetRunnerScaleSet(ctx, groupID, c.Name)
	if err != nil {
		return fmt.Errorf("get scale set: %w", err)
	}
	if set == nil {
		set, err = gh.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
			Name:          c.Name,
			RunnerGroupID: groupID,
			Labels:        labels,
			RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
		})
		if err != nil {
			return fmt.Errorf("create scale set: %w", err)
		}
	} else if err := validateExistingScaleSet(set, groupID, labels); err != nil {
		return fmt.Errorf("existing scale set: %w", err)
	}

	gh.SetSystemInfo(scaleset.SystemInfo{
		System:      "runnerhub",
		Subsystem:   "scaleset",
		Version:     "0.3.0",
		CommitSHA:   "unknown",
		ScaleSetID:  set.ID,
	})

	hub := runnerHubClient{
		base:   c.ControlURL,
		token:  c.ControlToken,
		poolID: c.PoolID,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
	limit, err := hub.poolLimit(ctx)
	if err != nil {
		return fmt.Errorf("runnerhub pool: %w", err)
	}
	if limit > 0 && c.MaxRunners > limit {
		slog.Warn("clamping scale-set max runners to pool max concurrency", "configured", c.MaxRunners, "poolMax", limit, "pool", c.PoolID)
		c.MaxRunners = limit
	}
	if c.MinRunners > c.MaxRunners {
		return errors.New("min runners exceeds effective pool maximum")
	}

	names, err := hub.activeWorkers(ctx)
	if err != nil {
		return fmt.Errorf("recover active workers: %w", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "runnerhub"
	}
	sessions, err := gh.MessageSessionClient(ctx, set.ID, hostname)
	if err != nil {
		return fmt.Errorf("message session: %w", err)
	}
	defer sessions.Close(context.Background())

	l, err := listener.New(sessions, listener.Config{ScaleSetID: set.ID, MaxRunners: c.MaxRunners, Logger: slog.Default()})
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	sc := &scaler{
		names:      names,
		min:        c.MinRunners,
		max:        c.MaxRunners,
		scaleSetID: set.ID,
		github:     gh,
		hub:        hub,
		logger:     slog.Default(),
	}

	slog.Info("scale-set listener ready", "name", c.Name, "scaleSetID", set.ID, "pool", c.PoolID, "maxRunners", c.MaxRunners)
	if err := l.Run(ctx, sc); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}

func validateExistingScaleSet(set *scaleset.RunnerScaleSet, groupID int, want []scaleset.Label) error {
	if set.RunnerGroupID != groupID {
		return fmt.Errorf("runner group mismatch: existing=%d configured=%d", set.RunnerGroupID, groupID)
	}
	gotNames := make([]string, 0, len(set.Labels))
	wantNames := make([]string, 0, len(want))
	for _, l := range set.Labels {
		gotNames = append(gotNames, l.Name)
	}
	for _, l := range want {
		wantNames = append(wantNames, l.Name)
	}
	sort.Strings(gotNames)
	sort.Strings(wantNames)
	if strings.Join(gotNames, "\x00") != strings.Join(wantNames, "\x00") {
		return fmt.Errorf("label mismatch: existing=%v configured=%v", gotNames, wantNames)
	}
	return nil
}

func activeState(s string) bool {
	switch s {
	case "requested", "provisioning", "starting", "registering", "ready", "busy":
		return true
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func intEnv(k string, d int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", k, err)
	}
	return n, nil
}

func boolEnv(k string, d bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", k)
	}
	return b, nil
}

func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requireProtectedURL(raw string, allow bool, label string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s URL is invalid", label)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && (allow || loopbackURL(raw))) {
		return nil
	}
	return fmt.Errorf("%s URL must use https; RUNNERHUB_ALLOW_INSECURE_HTTP is only for separately protected networks", label)
}

func repoName(owner, repo string) string {
	if owner == "" {
		return repo
	}
	if repo == "" {
		return owner
	}
	return owner + "/" + repo
}
