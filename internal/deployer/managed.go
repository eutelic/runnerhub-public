package deployer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	managementPrefix = "/api/v1/managed-apps"
	managementTimestampHeader = "X-RunnerHub-Management-Timestamp"
	managementSignatureHeader = "X-RunnerHub-Management-Signature"
)

type ManagedApp struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Enabled   bool   `json:"enabled"`
	Protected bool   `json:"protected"`
}

type managedSettings struct {
	Enabled []string `json:"enabled"`
}

type trueNASAppCatalog interface {
	ListApps(context.Context) ([]TrueNASApp, error)
}

// SignManagementRequest authenticates control-plane requests to the standalone
// deployer. The TrueNAS API key stays on the two trusted services and is never
// placed in an HTTP header. Workflow deployment tokens cannot sign requests.
func SignManagementRequest(req *http.Request, key []byte, body []byte) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(managementTimestampHeader, timestamp)
	req.Header.Set(managementSignatureHeader, managementSignature(key, req.Method, req.URL.EscapedPath(), timestamp, body))
}

func managementSignature(key []byte, method, path, timestamp string, body []byte) string {
	digest := sha256.Sum256(body)
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "runnerhub-managed-apps-v1\n%s\n%s\n%s\n%x", method, path, timestamp, digest)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) authenticatedManagementBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if len(s.ManagementKey) == 0 || s.ManagedAppsPath == "" {
		http.Error(w, "management disabled", http.StatusServiceUnavailable)
		return nil, false
	}
	timestamp := r.Header.Get(managementTimestampHeader)
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || unix < time.Now().Unix()-60 || unix > time.Now().Unix()+60 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	provided, err := hex.DecodeString(r.Header.Get(managementSignatureHeader))
	if err != nil || len(provided) != sha256.Size {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return nil, false
	}
	expected, _ := hex.DecodeString(managementSignature(s.ManagementKey, r.Method, r.URL.EscapedPath(), timestamp, body))
	if !hmac.Equal(provided, expected) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (s *Service) loadManaged() (map[string]bool, error) {
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	result := make(map[string]bool)
	data, err := os.ReadFile(s.ManagedAppsPath)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var settings managedSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("invalid managed app settings: %w", err)
	}
	for _, name := range settings.Enabled {
		if !appNamePattern.MatchString(name) {
			return nil, errors.New("invalid app name in managed settings")
		}
		result[name] = true
	}
	return result, nil
}

func (s *Service) setManaged(name string, enabled bool) error {
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	current := managedSettings{}
	data, err := os.ReadFile(s.ManagedAppsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("invalid managed app settings: %w", err)
		}
	}
	known := make(map[string]bool, len(current.Enabled))
	for _, app := range current.Enabled {
		if !appNamePattern.MatchString(app) {
			return errors.New("invalid app name in managed settings")
		}
		known[app] = true
	}
	if enabled {
		known[name] = true
	} else {
		delete(known, name)
	}
	current.Enabled = current.Enabled[:0]
	for app := range known {
		current.Enabled = append(current.Enabled, app)
	}
	sort.Strings(current.Enabled)
	encoded, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.ManagedAppsPath), 0700); err != nil {
		return err
	}
	return writePrivateFile(s.ManagedAppsPath, append(encoded, '\n'))
}

func (s *Service) protectedApp(name string) bool {
	if name == defaultDeployerApp {
		return true
	}
	if s.Policy != nil {
		for _, target := range s.Policy.targets {
			if target.App == name {
				return true // Existing workflow deployment targets keep their own authentication and lifecycle.
			}
		}
	}
	return false
}

func (s *Service) managedCatalog(ctx context.Context) ([]ManagedApp, error) {
	catalog, ok := s.TrueNAS.(trueNASAppCatalog)
	if !ok {
		return nil, errors.New("TrueNAS app discovery is unavailable")
	}
	apps, err := catalog.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := s.loadManaged()
	if err != nil {
		return nil, err
	}
	result := make([]ManagedApp, 0, len(apps))
	for _, app := range apps {
		if !appNamePattern.MatchString(app.Name) {
			continue
		}
		result = append(result, ManagedApp{
			Name:      app.Name,
			State:     app.State,
			Enabled:   enabled[app.Name],
			Protected: s.protectedApp(app.Name),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Service) listManagedApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.authenticatedManagementBody(w, r); !ok {
		return
	}
	apps, err := s.managedCatalog(r.Context())
	if err != nil {
		http.Error(w, "discover TrueNAS apps: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (s *Service) manageApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, ok := s.authenticatedManagementBody(w, r)
	if !ok {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, managementPrefix+"/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || !appNamePattern.MatchString(parts[0]) || len(parts[0]) > 40 {
		http.NotFound(w, r)
		return
	}
	app, action := parts[0], parts[1]
	if s.protectedApp(app) {
		http.Error(w, "app is managed by the workflow deployer or is the deployer itself", http.StatusForbidden)
		return
	}
	apps, err := s.managedCatalog(r.Context())
	if err != nil {
		http.Error(w, "discover TrueNAS apps: "+err.Error(), http.StatusBadGateway)
		return
	}
	exists, enabled := false, false
	for _, item := range apps {
		if item.Name == app {
			exists, enabled = true, item.Enabled
			break
		}
	}
	if !exists {
		http.Error(w, "TrueNAS app not found", http.StatusNotFound)
		return
	}
	switch action {
	case "enabled":
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(body, &input); err != nil || input.Enabled == nil {
			http.Error(w, "enabled boolean required", http.StatusBadRequest)
			return
		}
		if err := s.setManaged(app, *input.Enabled); err != nil {
			http.Error(w, "save app setting: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": app, "enabled": *input.Enabled})
	case "redeploy":
		if len(body) != 0 {
			http.Error(w, "redeploy request body must be empty", http.StatusBadRequest)
			return
		}
		if !enabled {
			http.Error(w, "redeployment is not enabled for this app", http.StatusForbidden)
			return
		}
		lock := s.appLock(app)
		if !lock.TryLock() {
			http.Error(w, "deployment already in progress for app", http.StatusConflict)
			return
		}
		defer lock.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		record := deploymentRecord{ID: deploymentID(), Target: app, App: app, RequestedAt: time.Now().UTC(), Result: "failed"}
		err := s.TrueNAS.RedeployAndWait(ctx, app)
		record.CompletedAt = time.Now().UTC()
		if err == nil {
			record.Result = "success"
		} else {
			record.Detail = safeDetail(err)
		}
		if auditErr := s.appendAudit(record); auditErr != nil {
			http.Error(w, "deployment finished but audit persistence failed", http.StatusInternalServerError)
			return
		}
		if err != nil {
			http.Error(w, "TrueNAS redeploy failed: "+record.Detail, http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": app, "state": "deployed"})
	default:
		http.NotFound(w, r)
	}
}
