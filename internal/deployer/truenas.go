package deployer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type TrueNAS interface {
	RedeployAndWait(context.Context, string) error
}

type TrueNASConfig struct {
	Endpoint           string
	Username           string
	APIKey             string
	CAFile             string
	InsecureSkipVerify bool
}

type TrueNASClient struct {
	endpoint string
	username string
	apiKey   string
	dialer   websocket.Dialer
	nextID   atomic.Uint64
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func NewTrueNASClient(cfg TrueNASConfig) (*TrueNASClient, error) {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "wss" || u.Host == "" {
		return nil, errors.New("RUNNERHUB_TRUENAS_URL must be an absolute wss:// URL")
	}
	if cfg.Username == "" {
		return nil, errors.New("TrueNAS username is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("TrueNAS API key is required")
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 -- explicit operator opt-in for private/self-signed TrueNAS endpoints; CA pinning remains supported
	}
	if strings.TrimSpace(cfg.CAFile) != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TrueNAS CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("TrueNAS CA file did not contain a valid certificate")
		}
		tlsConfig.RootCAs = pool
	}

	return &TrueNASClient{
		endpoint: cfg.Endpoint,
		username: cfg.Username,
		apiKey:   cfg.APIKey,
		dialer: websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			TLSClientConfig:  tlsConfig,
		},
	}, nil
}

func (c *TrueNASClient) RedeployAndWait(ctx context.Context, app string) error {
	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil {
		return fmt.Errorf("connect to TrueNAS: %w", err)
	}
	defer conn.Close()

	var auth struct {
		ResponseType string `json:"response_type"`
	}
	if err := c.call(ctx, conn, "auth.login_ex", []any{
		map[string]any{
			"mechanism": "API_KEY_PLAIN",
			"username":  c.username,
			"api_key":   c.apiKey,
			"login_options": map[string]any{
				"user_info": false,
			},
		},
	}, &auth); err != nil {
		return fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if auth.ResponseType != "SUCCESS" {
		return fmt.Errorf("authenticate to TrueNAS: response_type=%s", auth.ResponseType)
	}

	// app.pull_images is a TrueNAS job. JSON-RPC job calls emit progress
	// notifications while running and only send the matching method response
	// once the job has completed. call() intentionally ignores unrelated
	// notifications until the response carrying this request ID arrives.
	if err := c.call(ctx, conn, "app.pull_images", []any{
		app,
		map[string]any{"redeploy": true},
	}, nil); err != nil {
		return fmt.Errorf("pull/redeploy TrueNAS app %q: %w", app, err)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var state struct {
			State string `json:"state"`
		}
		if err := c.call(ctx, conn, "app.get_instance", []any{app, map[string]any{}}, &state); err != nil {
			return fmt.Errorf("inspect TrueNAS app %q: %w", app, err)
		}
		switch state.State {
		case "RUNNING":
			return nil
		case "CRASHED", "STOPPED":
			return fmt.Errorf("TrueNAS app %q entered state %s", app, state.State)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type trueNASAppInfo struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	State     string `json:"state"`
	CustomApp bool   `json:"custom_app"`
}

func (c *TrueNASClient) FindAppMount(ctx context.Context, app, destination string) (string, error) {
	app = strings.TrimSpace(app)
	destination = strings.TrimSpace(destination)
	if app == "" || destination == "" {
		return "", errors.New("TrueNAS app name and mount destination are required")
	}

	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("connect to TrueNAS: %w", err)
	}
	defer conn.Close()

	var auth struct {
		ResponseType string `json:"response_type"`
	}
	if err := c.call(ctx, conn, "auth.login_ex", []any{
		map[string]any{
			"mechanism": "API_KEY_PLAIN",
			"username":  c.username,
			"api_key":   c.apiKey,
			"login_options": map[string]any{
				"user_info": false,
			},
		},
	}, &auth); err != nil {
		return "", fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if auth.ResponseType != "SUCCESS" {
		return "", fmt.Errorf("authenticate to TrueNAS: response_type=%s", auth.ResponseType)
	}

	var instance struct {
		ActiveWorkloads struct {
			ContainerDetails []struct {
				ServiceName  string `json:"service_name"`
				VolumeMounts []struct {
					Source      string `json:"source"`
					Destination string `json:"destination"`
					Type        string `json:"type"`
				} `json:"volume_mounts"`
			} `json:"container_details"`
		} `json:"active_workloads"`
	}
	if err := c.call(ctx, conn, "app.get_instance", []any{app, map[string]any{}}, &instance); err != nil {
		return "", fmt.Errorf("inspect TrueNAS app %q: %w", app, err)
	}

	find := func(serviceOnly bool) string {
		for _, container := range instance.ActiveWorkloads.ContainerDetails {
			if serviceOnly && container.ServiceName != "control" {
				continue
			}
			for _, mount := range container.VolumeMounts {
				if mount.Destination != destination || mount.Source == "" {
					continue
				}
				if strings.HasPrefix(mount.Source, "/mnt/") {
					return filepath.Clean(mount.Source)
				}
			}
		}
		return ""
	}
	if source := find(true); source != "" {
		return source, nil
	}
	if source := find(false); source != "" {
		return source, nil
	}
	return "", fmt.Errorf("TrueNAS app %q does not expose a host /mnt mount for %s", app, destination)
}

func (c *TrueNASClient) EnsureCustomApp(ctx context.Context, app, compose string) error {
	app = strings.TrimSpace(app)
	compose = strings.TrimSpace(compose)
	if app == "" || compose == "" {
		return errors.New("TrueNAS custom app name and Compose configuration are required")
	}

	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil {
		return fmt.Errorf("connect to TrueNAS: %w", err)
	}
	defer conn.Close()

	var auth struct {
		ResponseType string `json:"response_type"`
	}
	if err := c.call(ctx, conn, "auth.login_ex", []any{
		map[string]any{
			"mechanism": "API_KEY_PLAIN",
			"username":  c.username,
			"api_key":   c.apiKey,
			"login_options": map[string]any{
				"user_info": false,
			},
		},
	}, &auth); err != nil {
		return fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if auth.ResponseType != "SUCCESS" {
		return fmt.Errorf("authenticate to TrueNAS: response_type=%s", auth.ResponseType)
	}

	var apps []trueNASAppInfo
	if err := c.call(ctx, conn, "app.query", []any{[]any{}, map[string]any{}}, &apps); err != nil {
		return fmt.Errorf("query TrueNAS apps: %w", err)
	}
	exists := false
	for _, candidate := range apps {
		if candidate.Name != app && candidate.ID != app {
			continue
		}
		if !candidate.CustomApp {
			return fmt.Errorf("TrueNAS app %q already exists but is not a custom app", app)
		}
		exists = true
		break
	}

	if exists {
		if err := c.call(ctx, conn, "app.update", []any{
			app,
			map[string]any{"custom_compose_config_string": compose},
		}, nil); err != nil {
			return fmt.Errorf("update TrueNAS custom app %q: %w", app, err)
		}
	} else {
		if err := c.call(ctx, conn, "app.create", []any{
			map[string]any{
				"app_name":                     app,
				"custom_app":                   true,
				"custom_compose_config_string": compose,
			},
		}, nil); err != nil {
			return fmt.Errorf("create TrueNAS custom app %q: %w", app, err)
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var state struct {
			State string `json:"state"`
		}
		if err := c.call(ctx, conn, "app.get_instance", []any{app, map[string]any{}}, &state); err != nil {
			return fmt.Errorf("inspect TrueNAS app %q: %w", app, err)
		}
		switch state.State {
		case "RUNNING":
			return nil
		case "CRASHED", "STOPPED":
			return fmt.Errorf("TrueNAS app %q entered state %s", app, state.State)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *TrueNASClient) call(ctx context.Context, conn *websocket.Conn, method string, params any, out any) error {
	id := c.nextID.Add(1)
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.SetReadDeadline(deadline)
	}
	if err := conn.WriteJSON(request); err != nil {
		return err
	}

	for {
		var response rpcResponse
		if err := conn.ReadJSON(&response); err != nil {
			return err
		}
		if len(response.ID) == 0 {
			continue
		}
		var responseID uint64
		if err := json.Unmarshal(response.ID, &responseID); err != nil || responseID != id {
			continue
		}
		if response.Error != nil {
			msg := strings.TrimSpace(response.Error.Message)
			if msg == "" {
				msg = "TrueNAS method call failed"
			}
			return fmt.Errorf("rpc %s: %s (code %d)", method, msg, response.Error.Code)
		}
		if out == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}
}
