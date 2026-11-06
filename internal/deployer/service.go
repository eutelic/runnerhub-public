package deployer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Service struct {
	BaseContext context.Context
	Policy     *PolicySet
	TrueNAS    TrueNAS
	HTTPClient *http.Client
	AuditPath  string
	MaintenanceToken string
	ManagementKey []byte
	ManagedAppsPath string
	selfMu sync.Mutex
	selfActive map[string]bool
	selfOpsMu sync.Mutex
	selfOps sync.WaitGroup
	selfOpsClosing bool

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
	auditMu sync.Mutex
	managedMu sync.Mutex
}

// beginSelfRedeploy reserves a tracked operation before returning 202. The
// shutdown gate and WaitGroup Add share a mutex so shutdown cannot race a new
// operation into a Wait that has already observed zero work.
func (s *Service) beginSelfRedeploy() bool {
	s.selfOpsMu.Lock()
	defer s.selfOpsMu.Unlock()
	if s.selfOpsClosing {
		return false
	}
	s.selfOps.Add(1)
	return true
}

// StopSelfRedeployAdmissions prevents new detached deploy operations from
// starting while the HTTP server shuts down.
func (s *Service) StopSelfRedeployAdmissions() {
	s.selfOpsMu.Lock()
	s.selfOpsClosing = true
	s.selfOpsMu.Unlock()
}

// WaitForSelfRedeploys waits for accepted operations, including their bounded
// recovery attempt after cancellation, until ctx expires.
func (s *Service) WaitForSelfRedeploys(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.selfOps.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	runIDPattern     = regexp.MustCompile(`^[0-9]+$`)
	imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type deploymentRequest struct {
	SHA         string `json:"sha"`
	RunID       string `json:"run_id"`
	ImageDigest string `json:"image_digest,omitempty"`
}

type deploymentRecord struct {
	ID          string    `json:"id"`
	Target      string    `json:"target"`
	App         string    `json:"app"`
	Repository  string    `json:"repository"`
	Image       string    `json:"image"`
	SHA         string    `json:"sha"`
	RunID       string    `json:"run_id"`
	RequestedAt time.Time `json:"requested_at"`
	CompletedAt time.Time `json:"completed_at"`
	Result      string    `json:"result"`
	Detail      string    `json:"detail,omitempty"`
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("GET /api/v1/deployments/{target}/status",s.handleDeploymentStatus)
	mux.HandleFunc("GET /api/v1/deployments/{target}/capabilities",s.handleDeploymentCapabilities)
	mux.HandleFunc("/api/v1/deployments/", s.handleDeploy)
	mux.HandleFunc("GET "+managementPrefix, s.listManagedApps)
	mux.HandleFunc("POST "+managementPrefix+"/", s.manageApp)
	return mux
}

func (s *Service) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	targetID := strings.TrimPrefix(r.URL.Path, "/api/v1/deployments/")
	if targetID == "" || strings.Contains(targetID, "/") {
		http.NotFound(w, r)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	target, ok := s.Policy.Authenticate(targetID, token)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req deploymentRequest
	body := io.LimitReader(r.Body, 32<<10)
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.SHA = bounded(strings.ToLower(req.SHA), 100)
	req.RunID = bounded(req.RunID, 100)
	if !commitSHAPattern.MatchString(req.SHA) || !runIDPattern.MatchString(req.RunID) {
		http.Error(w, "sha must be a 40-character commit SHA and run_id must be numeric", http.StatusBadRequest)
		return
	}
	req.ImageDigest = strings.ToLower(strings.TrimSpace(req.ImageDigest))
	if target.SelfRedeploy && !imageDigestPattern.MatchString(req.ImageDigest) {
		http.Error(w, "image_digest must be a sha256 digest for the published RunnerHub control image", http.StatusBadRequest)
		return
	}

	lock := s.appLock(target.App)
	if !lock.TryLock() {
		http.Error(w, "deployment already in progress for target app", http.StatusConflict)
		return
	}

	record := deploymentRecord{
		ID:          deploymentID(),
		Target:      target.ID,
		App:         target.App,
		Repository:  target.Repository,
		Image:       target.Image,
		SHA:         req.SHA,
		RunID:       req.RunID,
		RequestedAt: time.Now().UTC(),
		Result:      "failed",
	}
	if target.SelfRedeploy {
		record.Image = "ghcr.io/eutelic/runnerhub-control@" + req.ImageDigest
		if strings.TrimSpace(s.MaintenanceToken)==""{lock.Unlock();http.Error(w,"RunnerHub maintenance token is not configured",http.StatusServiceUnavailable);return}
		if existing,readErr:=s.readSelfStatus(target.ID);readErr==nil&&ongoingSelfDeployment(existing.Result){
            // The mutex is process-local. Following a deployer restart, old
            // "draining" state must not authorize a new update until the
            // persisted RunnerHub maintenance gate has been explicitly
            // restored by an administrator.
            checkCtx,cancel:=context.WithTimeout(r.Context(),10*time.Second)
            remote,checkErr:=s.maintenanceCall(checkCtx,target,http.MethodGet,"/api/v1/maintenance",nil)
            cancel()
            if checkErr!=nil||remote.Paused {
                lock.Unlock()
                http.Error(w,"previous self-redeployment needs recovery; inspect maintenance status and resume safely before retrying",http.StatusConflict)
                return
            }
            existing.Result="recovery_required"
            existing.Detail="Previous deployer process interrupted; admissions verified resumed before retry"
            existing.CompletedAt=time.Now().UTC()
            if err:=s.writeSelfStatus(existing);err!=nil{
                lock.Unlock()
                http.Error(w,"could not record recovery from interrupted self-redeployment",500)
                return
            }
        }
		if !s.beginSelfRedeploy() {
			lock.Unlock()
			http.Error(w, "deployer is shutting down; retry after it is healthy", http.StatusServiceUnavailable)
			return
		}
		record.Result="queued"
		if err:=s.writeSelfStatus(record);err!=nil{s.selfOps.Done();lock.Unlock();http.Error(w,"could not persist self-redeployment request",500);return}
		s.setSelfRunning(target.ID,true)
		operationContext := s.BaseContext
		if operationContext == nil {
			operationContext = context.WithoutCancel(r.Context())
		}
		go func() {
			defer s.selfOps.Done()
			s.runSelfRedeploy(operationContext,target,record,lock)
		}()
		writeJSON(w,http.StatusAccepted,map[string]any{"deployment_id":record.ID,"target":target.ID,"app":target.App,"state":"queued","status_url":"/api/v1/deployments/"+target.ID+"/status"})
		return
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), target.Timeout())
	defer cancel()

	err := s.TrueNAS.RedeployAndWait(ctx, target.App)
	if err == nil && target.HealthURL != "" {
		err = s.waitForHealth(ctx, target.HealthURL)
	}
	record.CompletedAt = time.Now().UTC()
	if err == nil {
		record.Result = "success"
	} else {
		record.Detail = safeDetail(err)
	}
	if auditErr := s.appendAudit(record); auditErr != nil {
		http.Error(w, "deployment completed but audit persistence failed", http.StatusInternalServerError)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"deployment_id": record.ID,
			"target":        target.ID,
			"app":           target.App,
			"repository":    target.Repository,
			"image":         target.Image,
			"state":         "failed",
			"detail":        record.Detail,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment_id": record.ID,
		"target":        target.ID,
		"app":           target.App,
		"repository":    target.Repository,
		"image":         target.Image,
		"state":         "deployed",
	})
}

func (s *Service) waitForHealth(ctx context.Context, healthURL string) error {
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("health check did not pass before timeout: %w", err)
			}
			return errors.New("health check did not return 200 before timeout")
		case <-ticker.C:
		}
	}
}

func (s *Service) appLock(app string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	if s.locks[app] == nil {
		s.locks[app] = &sync.Mutex{}
	}
	return s.locks[app]
}

func (s *Service) appendAudit(record deploymentRecord) error {
	if strings.TrimSpace(s.AuditPath) == "" {
		return errors.New("audit path is required")
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.AuditPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.AuditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(record)
}

func bearerToken(value string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func deploymentID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dep-%d", time.Now().UnixNano())
	}
	return "dep-" + hex.EncodeToString(b[:])
}

func bounded(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func safeDetail(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	if len(value) > 300 {
		value = value[:300]
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
