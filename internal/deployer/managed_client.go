package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The control service reaches its existing standalone deployer over the saved
// private-network URL. Requests are authenticated using a short-lived HMAC
// signed with the TrueNAS service key already persisted by bootstrap, without
// requiring the operator to paste new secrets into GitHub or the dashboard.
func (m *BootstrapManager) managedRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		root = defaultBootstrapRoot
	}
	stateBytes, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("deployer is not configured: %w", err)
	}
	var state bootstrapState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		return nil, fmt.Errorf("invalid deployer state: %w", err)
	}
	u, err := url.Parse(state.DeployerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid standalone deployer URL")
	}
	if !strings.HasPrefix(path, managementPrefix) || strings.ContainsAny(path, "?#") {
		return nil, errors.New("invalid management path")
	}
	key, err := os.ReadFile(filepath.Join(root, "secrets", "truenas-api-key"))
	if err != nil {
		return nil, fmt.Errorf("read deployer management credential: %w", err)
	}
	secret := []byte(strings.TrimSpace(string(key)))
	if len(secret) == 0 {
		return nil, errors.New("deployer management credential is empty")
	}

	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(u.String(), "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	SignManagementRequest(request, secret, body)
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 11 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("contact standalone TrueNAS deployer: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("standalone deployer returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (m *BootstrapManager) ManagedApps(ctx context.Context) ([]ManagedApp, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := m.managedRequest(ctx, http.MethodGet, managementPrefix, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Apps []ManagedApp `json:"apps"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("invalid deployer app listing: %w", err)
	}
	return result.Apps, nil
}

func (m *BootstrapManager) SetManagedApp(ctx context.Context, app string, enabled bool) error {
	if !appNamePattern.MatchString(app) || len(app) > 40 {
		return errors.New("invalid TrueNAS app name")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	_, err := m.managedRequest(ctx, http.MethodPost, managementPrefix+"/"+app+"/enabled", body)
	return err
}

func (m *BootstrapManager) RedeployManagedApp(ctx context.Context, app string) error {
	if !appNamePattern.MatchString(app) || len(app) > 40 {
		return errors.New("invalid TrueNAS app name")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute+15*time.Second)
	defer cancel()
	_, err := m.managedRequest(ctx, http.MethodPost, managementPrefix+"/"+app+"/redeploy", nil)
	return err
}
