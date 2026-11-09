package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/hashicorp/go-retryablehttp"
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
	rateLimit         *githubRateLimitTracker
}

type githubRateLimitSnapshot struct {
	Resource   string
	Limit      int64
	Remaining  int64
	Reset      time.Time
	RetryAfter time.Time
	StatusCode int
	RequestID  string
}

type githubRateLimitTracker struct {
	mu   sync.RWMutex
	snap githubRateLimitSnapshot
}

func (t *githubRateLimitTracker) observe(resp *http.Response) {
	if t == nil || resp == nil {
		return
	}
	s := githubRateLimitSnapshot{
		Resource:   strings.TrimSpace(resp.Header.Get("X-RateLimit-Resource")),
		Limit:      -1,
		Remaining:  -1,
		StatusCode: resp.StatusCode,
		RequestID:  strings.TrimSpace(resp.Header.Get("X-GitHub-Request-Id")),
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Limit")), 10, 64); err == nil {
		s.Limit = v
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")), 10, 64); err == nil {
		s.Remaining = v
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && v > 0 {
		s.Reset = time.Unix(v, 0)
	}
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
			s.RetryAfter = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if when, err := http.ParseTime(raw); err == nil {
			s.RetryAfter = when
		}
	}
	t.mu.Lock()
	t.snap = s
	t.mu.Unlock()
}

func (t *githubRateLimitTracker) cooldown(now time.Time) (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	t.mu.RLock()
	s := t.snap
	t.mu.RUnlock()
	until := s.RetryAfter
	if s.Remaining == 0 && s.Reset.After(until) {
		until = s.Reset
	}
	return until, until.After(now)
}

func (t *githubRateLimitTracker) detail(now time.Time) string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	s := t.snap
	t.mu.RUnlock()
	parts := []string{}
	if s.Resource != "" {
		parts = append(parts, "resource="+s.Resource)
	}
	if s.Remaining >= 0 {
		if s.Limit >= 0 {
			parts = append(parts, fmt.Sprintf("remaining=%d/%d", s.Remaining, s.Limit))
		} else {
			parts = append(parts, fmt.Sprintf("remaining=%d", s.Remaining))
		}
	}
	if until, ok := t.cooldown(now); ok {
		parts = append(parts, "cooldownUntil="+until.UTC().Format(time.RFC3339))
	}
	if s.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", s.StatusCode))
	}
	if s.RequestID != "" {
		parts = append(parts, "requestId="+s.RequestID)
	}
	return strings.Join(parts, " ")
}

// The upstream actions/scaleset client requires an *http.Transport (it type-asserts
// when applying TLS/proxy options). Do not replace retryablehttp's Transport with
// a RoundTripper wrapper: doing so panics during scale-set client construction.
// Instead, wait before each retryablehttp attempt using its request hook.
func waitForGitHubCooldown(ctx context.Context, tracker *githubRateLimitTracker) {
	if until, limited := tracker.cooldown(time.Now()); limited {
		timer := time.NewTimer(time.Until(until))
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
}

func githubRetryBackoff(minWait, maxWait time.Duration, attempt int, resp *http.Response) time.Duration {
	wait := retryablehttp.DefaultBackoff(minWait, maxWait, attempt, resp)
	if resp == nil {
		return wait
	}
	now := time.Now()
	var explicit time.Time
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
			explicit = now.Add(time.Duration(seconds) * time.Second)
		} else if when, err := http.ParseTime(raw); err == nil {
			explicit = when
		}
	}
	if remaining, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")), 10, 64); err == nil && remaining == 0 {
		if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			when := time.Unix(reset, 0)
			if when.After(explicit) {
				explicit = when
			}
		}
	}
	if explicit.After(now) && explicit.Sub(now) > wait {
		return explicit.Sub(now)
	}
	return wait
}

func newGitHubRetryClient(tracker *githubRateLimitTracker) *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 3
	client.RetryWaitMin = time.Second
	client.RetryWaitMax = 5 * time.Minute
	client.Backoff = githubRetryBackoff
	// Keep the original retry transport's rate-limit hooks; the SDK uses its own transport interface.
	client.RequestLogHook = func(_ retryablehttp.Logger, req *http.Request, _ int) {
		waitForGitHubCooldown(req.Context(), tracker)
	}
	client.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		tracker.observe(resp)
		// GitHub also reports installation rate limiting as HTTP 403, not
		// only 429. Retry those responses according to reset/retry headers,
		// but never retry ordinary permission-related 403s.
		if resp != nil && resp.StatusCode == http.StatusForbidden &&
			(strings.TrimSpace(resp.Header.Get("Retry-After")) != "" ||
				strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0") {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return true, nil
		}
		return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	}
	return client
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
		// #nosec G703 -- this path is supplied by trusted service configuration.
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("read github app key: %w", err)
		}
		c.AppPrivateKey = string(b)
	}
	if c.RunnerGroup == "" {
		c.RunnerGroup = scaleset.DefaultRunnerGroup
	}
	c.rateLimit = &githubRateLimitTracker{}
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
	tracker := c.rateLimit
	if tracker == nil {
		tracker = &githubRateLimitTracker{}
	}
	// v0.5.0 accepts a standard HTTP client and owns its own retry layer.
	// Keep the existing rate-limit-aware retry transport to preserve the
	// persistent installation cooldown and server Retry-After behavior.
	retryClient := newGitHubRetryClient(tracker).StandardClient()
	retryClient.Timeout = scaleset.DefaultTimeout
	httpOptions := []scaleset.HTTPOption{
		scaleset.WithHTTPClient(retryClient),
		scaleset.WithRetry(scaleset.RetryConfig{}), // retryClient already retries
	}
	app := scaleset.GitHubAppAuth{ClientID: c.AppClientID, InstallationID: c.AppInstallationID, PrivateKey: c.AppPrivateKey}
	if app.Validate() == nil {
		return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
			GitHubConfigURL: c.RegistrationURL,
			GitHubAppAuth:   app,
			SystemInfo:      info,
		}, httpOptions...)
	}
	if c.PAT == "" {
		return nil, errors.New("github app credentials or RUNNERHUB_GITHUB_TOKEN are required")
	}
	return scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     c.RegistrationURL,
		PersonalAccessToken: c.PAT,
		SystemInfo:          info,
	}, httpOptions...)
}

type runnerHubClient struct {
	base   string
	token  string
	poolID string
	http   *http.Client
}

type workerResponse struct {
	ID             string `json:"id"`
	PoolID         string `json:"poolId"`
	RunnerName     string `json:"runnerName"`
	GitHubRunnerID int64  `json:"githubRunnerId,omitempty"`
	State          string `json:"state"`
}

type poolResponse struct {
	ID             string `json:"id"`
	MaxConcurrency int    `json:"maxConcurrency"`
}

type poolHealthResponse struct {
	Healthy       bool     `json:"healthy"`
	Reasons       []string `json:"reasons,omitempty"`
	CapacityLimit *int     `json:"capacityLimit"`
}

type runnerHubHTTPError struct {
	Path       string
	StatusCode int
	Body       string
}

func (e *runnerHubHTTPError) Error() string {
	return fmt.Sprintf("runnerhub %s returned %d: %s", e.Path, e.StatusCode, e.Body)
}

func isRunnerHubConflict(err error) bool {
	var httpErr *runnerHubHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict
}

type scaleSetRunnerClient interface {
	GenerateJitRunnerConfig(context.Context, *scaleset.RunnerScaleSetJitRunnerSetting, int) (*scaleset.RunnerScaleSetJitRunnerConfig, error)
	RemoveRunner(context.Context, int64) error
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

func (c runnerHubClient) create(ctx context.Context, name, jit string, githubRunnerID int64) (workerResponse, error) {
	var out workerResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/pools/"+c.poolID+"/scale-workers", map[string]any{
		"runnerName":     name,
		"jitConfig":      jit,
		"githubRunnerId": githubRunnerID,
	}, &out)
	return out, err
}

func (c runnerHubClient) event(ctx context.Context, e scaleEvent) error {
	e.PoolID = c.poolID
	return c.do(ctx, http.MethodPost, "/api/v1/scale-events", e, nil)
}

func (c runnerHubClient) activeWorkers(ctx context.Context) (map[string]string, error) {
	var workers []workerResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/workers?excludeDeleted=true", nil, &workers); err != nil {
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

func (c runnerHubClient) poolHasCapacity(ctx context.Context) (bool, []string, error) {
	var health poolHealthResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/pools/"+c.poolID+"/health", nil, &health); err != nil {
		return false, nil, err
	}
	return health.Healthy, health.Reasons, nil
}

func (c runnerHubClient) scaleSetStatus(ctx context.Context, poolID, state, detail string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/scalesets/status", map[string]string{
		"poolId": poolID,
		"state":  state,
		"detail": detail,
	}, nil)
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
		return &runnerHubHTTPError{Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type jobAcquirer interface {
	AcquireJobs(context.Context, []int64) ([]int64, error)
}

type scaler struct {
	mu         sync.Mutex
	names      map[string]string
	min        int
	max        int
	scaleSetID int
	github     scaleSetRunnerClient
	jobs       jobAcquirer
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

// Scale implements the v0.5.0 listener protocol. Acknowledgement happens in
// the SDK only after this method succeeds, so all operations must be retryable.
func (s *scaler) Scale(ctx context.Context, message *scaleset.RunnerScaleSetMessage) error {
	if message == nil {
		return nil // a long-poll timeout has no new scale-set state
	}
	if len(message.JobAvailableMessages) > 0 {
		if s.jobs == nil {
			return errors.New("scale-set job acquirer is not configured")
		}
		ids := make([]int64, 0, len(message.JobAvailableMessages))
		for _, job := range message.JobAvailableMessages {
			if job == nil {
				continue
			}
			ids = append(ids, job.RunnerRequestID)
		}
		if len(ids) > 0 {
			if _, err := s.jobs.AcquireJobs(ctx, ids); err != nil {
				return fmt.Errorf("acquire scale-set jobs: %w", err)
			}
		}
	}
	for _, job := range message.JobStartedMessages {
		if job == nil {
			continue
		}
		if err := s.HandleJobStarted(ctx, job); err != nil {
			s.logger.Error("scale-set started-job callback failed",
				"pool", s.hub.poolID, "runner", job.RunnerName,
				"workflowRunID", job.WorkflowRunID, "jobID", job.JobID,
				"error", err)
			return fmt.Errorf("handle started job: %w", err)
		}
	}
	for _, job := range message.JobCompletedMessages {
		if job == nil {
			continue
		}
		if err := s.HandleJobCompleted(ctx, job); err != nil {
			return fmt.Errorf("handle completed job: %w", err)
		}
	}
	if message.Statistics != nil {
		if _, err := s.HandleDesiredRunnerCount(ctx, message.Statistics.TotalAssignedJobs); err != nil {
			return fmt.Errorf("reconcile scale-set desired runners: %w", err)
		}
	}
	return nil
}

// desiredRunnerTarget bounds warm capacity before adding assigned jobs.
// Avoid integer overflow when GitHub reports an unexpectedly large count,
// and never let malformed negative counts reduce the configured warm floor.
func desiredRunnerTarget(minIdle, maxRunners, assigned int) int {
	if maxRunners <= 0 {
		return 0
	}
	if minIdle < 0 {
		minIdle = 0
	}
	if minIdle > maxRunners {
		minIdle = maxRunners
	}
	if assigned < 0 {
		assigned = 0
	}
	if assigned >= maxRunners-minIdle {
		return maxRunners
	}
	return minIdle + assigned
}

func (s *scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	// RunnerHub independently reconciles failed/disappeared provider workers. Re-read
	// active state on every desired-count delivery so a runner that died before job
	// assignment cannot remain stuck in this listener's local capacity count.
	if err := s.syncActiveWorkers(ctx); err != nil {
		return s.count(), fmt.Errorf("refresh RunnerHub active workers: %w", err)
	}

	target := desiredRunnerTarget(s.min, s.max, count)
	for s.count() < target {
		hasCapacity, reasons, err := s.hub.poolHasCapacity(ctx)
		if err != nil {
			return s.count(), fmt.Errorf("check RunnerHub pool capacity: %w", err)
		}
		if !hasCapacity {
			s.logger.Info("waiting for RunnerHub capacity", "pool", s.hub.poolID, "active", s.count(), "target", target, "reasons", reasons)
			break
		}

		name := fmt.Sprintf("runnerhub-%d-%d", time.Now().UnixNano(), s.count())
		jit, err := s.github.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name}, s.scaleSetID)
		if err != nil {
			return s.count(), fmt.Errorf("generate jit: %w", err)
		}
		if jit == nil || jit.Runner == nil || jit.Runner.ID <= 0 || strings.TrimSpace(jit.EncodedJITConfig) == "" {
			return s.count(), errors.New("github returned incomplete scale-set jit configuration")
		}

		runnerID := int64(jit.Runner.ID)
		wr, err := s.hub.create(ctx, name, jit.EncodedJITConfig, runnerID)
		if err == nil && (wr.ID == "" || wr.PoolID != s.hub.poolID || wr.RunnerName != name ||
			wr.GitHubRunnerID != runnerID || !activeState(wr.State)) {
			// A stale/replayed 202 response must never become a phantom
			// active runner in the scale-set listener's local accounting.
			err = fmt.Errorf("RunnerHub returned an inactive or mismatched scale worker response")
		}
		if err != nil {
			// The request may have reached RunnerHub even if the response was lost.
			// Recover by runner name before removing the GitHub JIT registration.
			if active, recoverErr := s.hub.activeWorkers(ctx); recoverErr == nil {
				if workerID := active[name]; workerID != "" {
					s.mu.Lock()
					s.names[name] = workerID
					s.mu.Unlock()
					s.logger.Warn("recovered RunnerHub worker after ambiguous create response", "pool", s.hub.poolID, "runner", name, "worker", workerID, "error", err)
					continue
				}
			}

			cleanupErr := s.github.RemoveRunner(context.WithoutCancel(ctx), runnerID)
			if cleanupErr != nil {
				return s.count(), fmt.Errorf("provision RunnerHub worker: %w; cleanup generated GitHub runner %d: %v", err, runnerID, cleanupErr)
			}
			if isRunnerHubConflict(err) {
				s.logger.Info("RunnerHub capacity changed before provisioning; cleaned generated GitHub runner and will retry", "pool", s.hub.poolID, "runner", name)
				break
			}
			return s.count(), fmt.Errorf("provision RunnerHub worker: %w", err)
		}
		s.mu.Lock()
		s.names[name] = wr.ID
		s.mu.Unlock()
		s.logger.Info("provisioning runner", "runner", name, "worker", wr.ID, "githubRunnerID", runnerID)
	}
	return s.count(), nil
}

func (s *scaler) HandleJobStarted(ctx context.Context, info *scaleset.JobStarted) error {
	started := time.Now().UTC()
	err := s.hub.event(ctx, scaleEvent{
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
	if err == nil {
		return nil
	}

	// GitHub may replay JobStarted after the ephemeral runner was cleaned up.
	// Never terminate the scale-set listener for a confirmed stale event.
	// Preserve errors for still-active workers and failed lookups so genuine
	// control-plane inconsistencies are not silently acknowledged.
	var httpErr *runnerHubHTTPError
	if !errors.As(err, &httpErr) || httpErr.Path != "/api/v1/scale-events" || httpErr.StatusCode != http.StatusNotFound {
		return err
	}
	active, lookupErr := s.hub.activeWorkers(ctx)
	if lookupErr != nil {
		return fmt.Errorf("verify missing scale-set worker: %w (active lookup: %v)", err, lookupErr)
	}
	if _, exists := active[info.RunnerName]; exists {
		return fmt.Errorf("started-job runner %q is active despite missing-worker response: %w", info.RunnerName, err)
	}
	s.mu.Lock()
	delete(s.names, info.RunnerName)
	s.mu.Unlock()
	s.logger.Warn("ignoring stale started-job event for removed worker",
		"pool", s.hub.poolID, "runner", info.RunnerName,
		"workflowRunID", info.WorkflowRunID, "jobID", info.JobID)
	return nil
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
	cancel         context.CancelFunc
	fingerprint    string
	ready          bool
	stopping       bool
	recoveryDetail string
}

type listenerRetryState struct {
	attempt      int
	nextAttempt  time.Time
	fingerprint  string
	lastFailure  string
	firstFailure time.Time
}

// Keep the last failure visible across manager reconciliation ticks, even
// when the listener is temporarily not running. The previous code replaced the
// original error with only a "restart delayed" timestamp on every tick.
func listenerRetryStatus(retry listenerRetryState) string {
	detail := fmt.Sprintf("listener retry attempt=%d at %s", retry.attempt, retry.nextAttempt.UTC().Format(time.RFC3339))
	if retry.lastFailure != "" {
		detail += "; last failure: " + retry.lastFailure
	}
	return detail
}

// Record a successful 409 recovery as a resolved incident, never as a warning.
// Only fixed failure classifications are accepted in server-backed status.
func listenerRecoveryStatus(retry listenerRetryState, at time.Time) string {
	if retry.attempt == 0 || retry.lastFailure != "GitHub message session conflict (409)" {
		return ""
	}
	elapsed := at.Sub(retry.firstFailure)
	if retry.firstFailure.IsZero() || elapsed < 0 {
		elapsed = 0
	}
	return fmt.Sprintf("recovered from transient GitHub message session conflict (409) after %d retries in %s at %s",
		retry.attempt, elapsed.Round(time.Second), at.UTC().Format(time.RFC3339))
}

var http409Token = regexp.MustCompile(`(?:^|[^a-z0-9])(?:(?:http(?:/[^ ]+)?[ :]+|status(?: code)?[ :=]+|response[ :]+)409|409[ :]+conflict)(?:[^0-9]|$)`)

func listenerFailureSummary(err error) string {
	if err == nil {
		return ""
	}
	// Never persist raw upstream errors: they can include credentials, URLs,
	// headers, or response bodies. Only fixed classifications are safe here.
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "scale-set worker not found"):
		return "scale-set worker not found (404)"
	case http409Token.MatchString(message) || strings.Contains(message, "message session conflict") || strings.Contains(message, "message-session conflict"):
		return "GitHub message session conflict (409)"
	case strings.Contains(message, "429"):
		return "upstream rate limited (429)"
	case strings.Contains(message, "403"):
		return "upstream authorization denied (403)"
	case strings.Contains(message, "404"):
		return "upstream resource not found (404)"
	default:
		return "listener operation failed (details withheld)"
	}
}

func beginListenerStop(active map[string]managedListener, key string) bool {
	running, ok := active[key]
	if !ok || running.stopping {
		return false
	}
	if running.cancel != nil {
		running.cancel()
	}
	running.ready = false
	running.stopping = true
	active[key] = running
	return true
}

func listenerMayStart(key string, active map[string]managedListener) bool {
	_, exists := active[key]
	return !exists
}

func listenerRetryDelay(attempt int, _ string) time.Duration {
	// Process-random jitter prevents separate managers retrying the same pool
	// on the same deterministic schedule after a broker session conflict.
	return listenerRetryDelayWithJitter(attempt, mathrand.Int63n)
}

func listenerRetryDelayWithJitter(attempt int, randomIntn func(int64) int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 10 * time.Second
	for i := 1; i < attempt && base < 5*time.Minute; i++ {
		base *= 2
		if base > 5*time.Minute {
			base = 5 * time.Minute
		}
	}
	jitterMax := base / 4
	if jitterMax <= 0 {
		return base
	}
	if randomIntn == nil {
		randomIntn = mathrand.Int63n
	}
	jitter := time.Duration(randomIntn(int64(jitterMax) + 1))
	if jitter < 0 || jitter > jitterMax {
		jitter = 0
	}
	if base+jitter > 5*time.Minute {
		return 5 * time.Minute
	}
	return base + jitter
}

type listenerResult struct {
	key         string
	fingerprint string
	ready       bool
	err         error
}

var errMessageSessionClose = errors.New("GitHub message session close failed")

func messageSessionCloseResult(runErr, closeErr error) error {
	if closeErr == nil {
		return runErr
	}
	closeErr = fmt.Errorf("%w: %w", errMessageSessionClose, closeErr)
	if runErr == nil {
		return closeErr
	}
	return errors.Join(runErr, closeErr)
}

// Never interpolate the upstream message-session close error into a
// server-backed status or audit field; it may include tokens or headers.
func blockedSessionCloseDetail(_ error) string {
	return "GitHub message session close failed; replacement blocked pending manager restart"
}

func blockListenerOnSessionCloseFailure(active map[string]managedListener, key string, resultErr error) bool {
	if !errors.Is(resultErr, errMessageSessionClose) {
		return false
	}
	running, ok := active[key]
	if !ok {
		return false
	}
	running.ready = false
	running.stopping = true
	active[key] = running
	return true
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
	if err := runListener(ctx, c, func() {
		if err := setReadiness(true); err != nil {
			slog.Error("publish scale-set readiness", "error", err)
		}
	}); err != nil && !errors.Is(err, context.Canceled) {
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
		// #nosec G703 -- this path is supplied by trusted service configuration.
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
	desired := map[string]managedConfig{}
	retries := map[string]listenerRetryState{}
	rateLimits := map[int64]*githubRateLimitTracker{}
	trackerFor := func(installationID int64) *githubRateLimitTracker {
		tracker := rateLimits[installationID]
		if tracker == nil {
			tracker = &githubRateLimitTracker{}
			rateLimits[installationID] = tracker
		}
		return tracker
	}
	results := make(chan listenerResult, 64)
	var listeners sync.WaitGroup
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	updateReadiness := func() {
		ready := allDesiredListenersReady(desired, active)
		if err := setReadiness(ready); err != nil {
			slog.Error("update scale-set manager readiness", "ready", ready, "error", err)
		}
	}

	reconcile := func() {
		if ctx.Err() != nil {
			return
		}
		var remote []managedConfig
		if err := hub.do(ctx, http.MethodGet, "/api/v1/scalesets/configs", nil, &remote); err != nil {
			_ = setReadiness(false)
			slog.Error("load dynamic scale-set configs", "error", err)
			return
		}
		nextDesired := map[string]managedConfig{}
		for _, item := range remote {
			if item.PoolID == "" || item.RegistrationURL == "" || item.Name == "" || item.AppInstallationID <= 0 {
				slog.Error("ignoring invalid dynamic scale-set config", "pool", item.PoolID, "registrationURL", item.RegistrationURL)
				continue
			}
			nextDesired[item.PoolID] = item
		}
		desired = nextDesired

		for key, running := range active {
			item, ok := desired[key]
			if !ok || configFingerprint(item) != running.fingerprint {
				if beginListenerStop(active, key) {
					delete(retries, key)
					if ok {
						_ = hub.scaleSetStatus(ctx, key, "starting", "waiting for the previous message session to close")
					}
				}
				continue
			}
			if running.stopping {
				continue
			}
			state := "starting"
			if running.ready {
				state = "ready"
			}
			detail := ""
			if running.ready {
				detail = running.recoveryDetail
			}
			_ = hub.scaleSetStatus(ctx, key, state, detail)
		}
		// Launch one new listener per GitHub App installation per cycle.
		// Starting every configured repository at once produces a burst of
		// scale-set auth/discovery calls on shared GitHub App credentials.
		// Stable ordering also makes startup progression observable.
		poolIDs := make([]string, 0, len(desired))
		for key := range desired {
			poolIDs = append(poolIDs, key)
		}
		sort.Strings(poolIDs)
		startedInstallations := map[int64]bool{}
		for _, key := range poolIDs {
			item := desired[key]
			if !listenerMayStart(key, active) {
				continue
			}
			fingerprint := configFingerprint(item)
			if retry, ok := retries[key]; ok {
				if retry.fingerprint != fingerprint {
					delete(retries, key)
				} else if retry.nextAttempt.After(time.Now()) {
					_ = hub.scaleSetStatus(ctx, key, "backing_off", listenerRetryStatus(retry))
					continue
				}
			}
			tracker := trackerFor(item.AppInstallationID)
			if until, ok := tracker.cooldown(time.Now()); ok {
				detail := "GitHub rate-limit cooldown until " + until.UTC().Format(time.RFC3339)
				if rateDetail := tracker.detail(time.Now()); rateDetail != "" {
					detail += " (" + rateDetail + ")"
				}
				_ = hub.scaleSetStatus(ctx, key, "backing_off", detail)
				continue
			}
			if startedInstallations[item.AppInstallationID] {
				continue
			}
			cfg := configFromManaged(base, item)
			cfg.rateLimit = tracker
			childCtx, childCancel := context.WithCancel(ctx)
			active[key] = managedListener{cancel: childCancel, fingerprint: fingerprint}
			startedInstallations[item.AppInstallationID] = true
			_ = hub.scaleSetStatus(ctx, key, "starting", "")
			slog.Info("starting managed scale-set listener", "pool", key, "repository", item.RegistrationURL, "name", item.Name)
			listeners.Add(1)
			go func(key, fingerprint string, cfg config) {
				defer listeners.Done()
				sendResult := func(result listenerResult) {
					select {
					case results <- result:
					case <-ctx.Done():
					}
				}
				err := runListener(childCtx, cfg, func() {
					sendResult(listenerResult{key: key, fingerprint: fingerprint, ready: true})
				})
				sendResult(listenerResult{key: key, fingerprint: fingerprint, err: err})
			}(key, fingerprint, cfg)
		}
		updateReadiness()
		slog.Info("scale-set manager reconciled", "desired", len(desired), "active", len(active), "ready", allDesiredListenersReady(desired, active))
	}

	if err := setReadiness(false); err != nil {
		slog.Error("clear initial scale-set readiness", "error", err)
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			for _, running := range active {
				running.cancel()
			}
			_ = setReadiness(false)
			stopped := make(chan struct{})
			go func() {
				listeners.Wait()
				close(stopped)
			}()
			timer := time.NewTimer(8 * time.Second)
			defer timer.Stop()
			select {
			case <-stopped:
			case <-timer.C:
				slog.Warn("timed out waiting for scale-set message sessions to close during shutdown")
			}
			return ctx.Err()
		case <-ticker.C:
			reconcile()
		case result := <-results:
			running, ok := active[result.key]
			if !ok || running.fingerprint != result.fingerprint {
				continue
			}
			if result.ready {
				if running.stopping {
					continue
				}
				running.ready = true
				if retry, ok := retries[result.key]; ok && retry.fingerprint == result.fingerprint {
					running.recoveryDetail = listenerRecoveryStatus(retry, time.Now())
				}
				active[result.key] = running
				delete(retries, result.key)
				_ = hub.scaleSetStatus(ctx, result.key, "ready", running.recoveryDetail)
				updateReadiness()
				continue
			}
			if blockListenerOnSessionCloseFailure(active, result.key, result.err) {
				// Upstream Close errors can include request/credential material.
				// Never persist or log the raw error in a dashboard or audit entry.
				detail := blockedSessionCloseDetail(result.err)
				_ = hub.scaleSetStatus(ctx, result.key, "error", detail)
				slog.Error("blocking scale-set listener replacement after message session close failure", "pool", result.key, "failure", listenerFailureSummary(result.err))
				updateReadiness()
				continue
			}

			delete(active, result.key)
			if running.stopping {
				updateReadiness()
				// Do not start a replacement until runListener has returned, which
				// includes closing its GitHub message session.
				reconcile()
				continue
			}
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				prev := retries[result.key]
				attempt := prev.attempt + 1
				nextAttempt := time.Now().Add(listenerRetryDelay(attempt, result.key))
				if item, ok := desired[result.key]; ok {
					tracker := trackerFor(item.AppInstallationID)
					if until, limited := tracker.cooldown(time.Now()); limited && until.After(nextAttempt) {
						nextAttempt = until
					}
				}
				firstFailure := prev.firstFailure
				if firstFailure.IsZero() {
					firstFailure = time.Now()
				}
				retries[result.key] = listenerRetryState{
					attempt: attempt, nextAttempt: nextAttempt, fingerprint: result.fingerprint,
					lastFailure: listenerFailureSummary(result.err), firstFailure: firstFailure,
				}
				detail := listenerRetryStatus(retries[result.key])
				if item, ok := desired[result.key]; ok {
					if rateDetail := trackerFor(item.AppInstallationID).detail(time.Now()); rateDetail != "" {
						detail += " (" + rateDetail + ")"
					}
				}
				_ = hub.scaleSetStatus(ctx, result.key, "backing_off", detail)
				slog.Error("managed scale-set listener stopped; backing off", "pool", result.key, "retryAt", nextAttempt, "attempt", attempt, "failure", listenerFailureSummary(result.err))
			}
			updateReadiness()
		}
	}
}

func allDesiredListenersReady(desired map[string]managedConfig, active map[string]managedListener) bool {
	if len(desired) == 0 {
		return true
	}
	for key := range desired {
		listener, ok := active[key]
		if !ok || !listener.ready {
			return false
		}
	}
	return true
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

func runListener(ctx context.Context, c config, onReady func()) (runErr error) {
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
		System:     "runnerhub",
		Subsystem:  "scaleset",
		Version:    "0.3.0",
		CommitSHA:  "unknown",
		ScaleSetID: set.ID,
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

	// Validate capacity support before creating a GitHub message session.
	// Older controllers missing capacityLimit must be upgraded first.
	initialCapacity, err := hub.advertisedCapacity(ctx, c.MaxRunners)
	if err != nil {
		return fmt.Errorf("initial scale-set capacity: %w", err)
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
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sessions.Close(closeCtx); err != nil {
			slog.Warn("close GitHub scale-set message session", "scaleSetID", set.ID, "failure", listenerFailureSummary(err))
			runErr = messageSessionCloseResult(runErr, err)
		}
	}()

	l, err := listener.New(sessions, listener.Config{ScaleSetID: set.ID, MaxRunners: c.MaxRunners, Logger: slog.Default()})
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	l.SetMaxRunners(initialCapacity)
	capacityCtx, stopCapacity := context.WithCancel(ctx)
	defer stopCapacity()
	go refreshAdvertisedCapacity(capacityCtx, hub, c.MaxRunners, l, initialCapacity, 10*time.Second)
	sc := &scaler{
		names:      names,
		min:        c.MinRunners,
		max:        c.MaxRunners,
		scaleSetID: set.ID,
		github:     gh,
		jobs:       sessions,
		hub:        hub,
		logger:     slog.Default(),
	}

	slog.Info("scale-set listener ready", "name", c.Name, "scaleSetID", set.ID, "pool", c.PoolID, "maxRunners", c.MaxRunners, "advertisedCapacity", initialCapacity)
	if onReady != nil {
		onReady()
	}
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
