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
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type TrueNAS interface {
	RedeployAndWait(context.Context, string) error
}

type TrueNASImageRedeployer interface {
	RedeployImageAndWait(context.Context, string, string) error
}

// ErrAppInstallMayBeInProgress means a TrueNAS app create/update was submitted,
// but its eventual completion is uncertain. Do not delete file-backed secrets
// while TrueNAS may still try to bind mount them.
var ErrAppInstallMayBeInProgress = errors.New("TrueNAS app install may still be in progress")

type TrueNASConfig struct {
	Endpoint           string
	Username           string
	APIKey             string
	CAFile             string
	InsecureSkipVerify bool
}

type TrueNASClient struct {
	endpoint        string
	username        string
	apiKey          string
	dialer          websocket.Dialer
	nextID          atomic.Uint64
	appPollInterval time.Duration // test override; defaults to two seconds
	appWaitTimeout  time.Duration // test override; defaults to two minutes
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
	Config    struct {
		CustomComposeConfigString string `json:"custom_compose_config_string"`
	} `json:"config"`
}

func replaceRunnerHubControlImage(compose, image string) (string, error) {
	const repository = "ghcr.io/eutelic/runnerhub-control"
	if !strings.HasPrefix(image, repository+"@") ||
		!imageDigestPattern.MatchString(strings.TrimPrefix(image, repository+"@")) {
		return "", errors.New("RunnerHub control image must be pinned by SHA-256 digest")
	}
	const anchor = "control: &control-image "
	lines := strings.Split(compose, "\n")
	replaced := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, anchor) {
			continue
		}
		current := strings.TrimSpace(strings.TrimPrefix(trimmed, anchor))
		if !strings.HasPrefix(current, repository+":") && !strings.HasPrefix(current, repository+"@sha256:") {
			return "", errors.New("TrueNAS RunnerHub control image anchor does not use the expected image repository")
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + anchor + image
		replaced++
	}
	if replaced != 1 {
		return "", fmt.Errorf("expected one RunnerHub control image anchor in TrueNAS Compose config, found %d", replaced)
	}
	return strings.Join(lines, "\n"), nil
}

func pinRunnerHubComposeConfig(config map[string]any, image string) (map[string]any, error) {
 const repo="ghcr.io/eutelic/runnerhub-control"
 if !strings.HasPrefix(image,repo+"@")||!imageDigestPattern.MatchString(strings.TrimPrefix(image,repo+"@")){return nil,errors.New("RunnerHub control image requires an immutable SHA-256 digest")}
 if len(config)==0{return nil,errors.New("TrueNAS did not return inspectable Compose configuration")}
 raw,err:=json.Marshal(config);if err!=nil{return nil,err}
 var changed map[string]any
 if err:=json.Unmarshal(raw,&changed);err!=nil{return nil,err}
 if _,ok:=changed["include"];ok{return nil,errors.New("Compose include directives require manual review")}
 services,ok:=changed["services"].(map[string]any);if !ok{return nil,errors.New("no Compose services")}
 control,ok:=services["control"].(map[string]any);if !ok{return nil,errors.New("no RunnerHub control service")}
 current,ok:=control["image"].(string)
 if !ok||(!strings.HasPrefix(current,repo+":")&&!strings.HasPrefix(current,repo+"@sha256:")){return nil,errors.New("control image repository differs from RunnerHub")}
 for name,value:=range services{
   if name=="control"{continue}
   other,ok:=value.(map[string]any);if !ok{return nil,fmt.Errorf("invalid Compose service %s",name)}
   if otherImage,ok:=other["image"].(string);ok&&(strings.HasPrefix(otherImage,repo+":")||strings.HasPrefix(otherImage,repo+"@")){return nil,errors.New("control image also used by another service")}
 }
 control["image"]=image
 if ext,ok:=changed["x-runnerhub-images"].(map[string]any);ok{
   if old,ok:=ext["control"].(string);ok{
     if !strings.HasPrefix(old,repo+":")&&!strings.HasPrefix(old,repo+"@sha256:"){return nil,errors.New("unexpected control image extension")}
     ext["control"]=image
   }
 }
 return changed,nil
}

type pinnedRunnerHubApp struct {
 Name string `json:"name"`
 ID string `json:"id"`
 State string `json:"state"`
 CustomApp bool `json:"custom_app"`
 Config map[string]any `json:"config"`
 ActiveWorkloads struct {
   ContainerDetails []struct {
     ServiceName string `json:"service_name"`
     Image string `json:"image"`
   } `json:"container_details"`
 } `json:"active_workloads"`
}

func (c *TrueNASClient) RedeployImageAndWait(ctx context.Context, app, image string) error {
 if !appNamePattern.MatchString(app)||len(app)>40{return errors.New("invalid TrueNAS app name")}
 if !strings.HasPrefix(image,"ghcr.io/eutelic/runnerhub-control@")||!imageDigestPattern.MatchString(strings.TrimPrefix(image,"ghcr.io/eutelic/runnerhub-control@")){return errors.New("control image must be an immutable SHA-256 digest")}
 conn,_,err:=c.dialer.DialContext(ctx,c.endpoint,nil);if err!=nil{return fmt.Errorf("connect to TrueNAS: %w",err)}
 defer conn.Close()
 var auth struct {ResponseType string `json:"response_type"`}
 if err:=c.call(ctx,conn,"auth.login_ex",[]any{map[string]any{"mechanism":"API_KEY_PLAIN","username":c.username,"api_key":c.apiKey,"login_options":map[string]any{"user_info":false}}},&auth);err!=nil{return fmt.Errorf("authenticate to TrueNAS: %w",err)}
 if auth.ResponseType!="SUCCESS"{return errors.New("TrueNAS authentication failed")}
 readApp:=func()(pinnedRunnerHubApp,error){
   var info pinnedRunnerHubApp
   err:=c.call(ctx,conn,"app.get_instance",[]any{app,map[string]any{"extra":map[string]any{"retrieve_config":true}}},&info)
   return info,err
 }
 before,err:=readApp();if err!=nil{return fmt.Errorf("inspect TrueNAS app: %w",err)}
 if (before.ID!=app&&before.Name!=app)||!before.CustomApp||before.State!="RUNNING"{return errors.New("self-redeployment requires a running custom TrueNAS app")}
 updated,err:=pinRunnerHubComposeConfig(before.Config,image);if err!=nil{return err}
 check,err:=readApp();if err!=nil{return fmt.Errorf("recheck TrueNAS Compose config: %w",err)}
 if !check.CustomApp||check.State!="RUNNING"||!reflect.DeepEqual(check.Config,before.Config){return errors.New("TrueNAS app config changed before pinned update")}
 if err:=c.call(ctx,conn,"app.update",[]any{app,map[string]any{"custom_compose_config":updated}},nil);err!=nil{return fmt.Errorf("update TrueNAS image: %w",err)}
 after,err:=readApp();if err!=nil{return fmt.Errorf("verify TrueNAS image update: %w",err)}
 if !after.CustomApp||after.State!="RUNNING"{return errors.New("TrueNAS app not RUNNING after update")}
 services,ok:=after.Config["services"].(map[string]any);if !ok{return errors.New("updated Compose config unavailable")}
 control,ok:=services["control"].(map[string]any);if !ok||control["image"]!=image{return errors.New("TrueNAS did not persist immutable control image")}
 count:=0
 for _,container:=range after.ActiveWorkloads.ContainerDetails {
   if container.ServiceName=="control" {
     if container.Image!=image {return errors.New("running control container does not match pinned digest")}
     count++
   }
 }
 if count!=1{return errors.New("TrueNAS did not report exactly one matching control container")}
 return nil
}

type TrueNASApp struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// ListApps returns the installed TrueNAS application inventory. The caller
// must authenticate as the management service; no TrueNAS credentials are
// returned to the web dashboard.
func (c *TrueNASClient) ListApps(ctx context.Context) ([]TrueNASApp, error) {
	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to TrueNAS: %w", err)
	}
	defer conn.Close()

	var auth struct {
		ResponseType string `json:"response_type"`
	}
	if err := c.call(ctx, conn, "auth.login_ex", []any{
		map[string]any{
			"mechanism": "API_KEY_PLAIN",
			"username": c.username,
			"api_key": c.apiKey,
			"login_options": map[string]any{"user_info": false},
		},
	}, &auth); err != nil {
		return nil, fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if auth.ResponseType != "SUCCESS" {
		return nil, fmt.Errorf("authenticate to TrueNAS: response_type=%s", auth.ResponseType)
	}

	var apps []trueNASAppInfo
	if err := c.call(ctx, conn, "app.query", []any{[]any{}, map[string]any{}}, &apps); err != nil {
		return nil, fmt.Errorf("query TrueNAS apps: %w", err)
	}
	result := make([]TrueNASApp, 0, len(apps))
	for _, app := range apps {
		result = append(result, TrueNASApp{Name: app.Name, State: app.State})
	}
	return result, nil
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
			return fmt.Errorf("%w: update TrueNAS custom app %q: %w", ErrAppInstallMayBeInProgress, app, err)
		}
	} else {
		if err := c.call(ctx, conn, "app.create", []any{
			map[string]any{
				"app_name":                     app,
				"custom_app":                   true,
				"custom_compose_config_string": compose,
			},
		}, nil); err != nil {
			return fmt.Errorf("%w: create TrueNAS custom app %q: %w", ErrAppInstallMayBeInProgress, app, err)
		}
	}

	// Creating a TrueNAS app is asynchronous. Querying get_instance before
	// it has registered produces Invalid params, which would cause bootstrap
	// rollback to remove the API-key file while TrueNAS is still starting it.
	// app.query tolerates a not-yet-created app and lets us wait for RUNNING.
	pollInterval := c.appPollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	waitTimeout := c.appWaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = 2 * time.Minute
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	for {
		var instances []trueNASAppInfo
		if err := c.call(ctx, conn, "app.query", []any{[]any{}, map[string]any{}}, &instances); err != nil {
			return fmt.Errorf("%w: inspect TrueNAS app %q: %w", ErrAppInstallMayBeInProgress, app, err)
		}
		for _, instance := range instances {
			if instance.Name != app && instance.ID != app {
				continue
			}
			switch instance.State {
			case "RUNNING":
				return nil
			case "CRASHED", "STOPPED", "ERROR":
				// TrueNAS reports a terminal state, so creation is no longer
				// pending; the caller can safely restore previous bootstrap files.
				return fmt.Errorf("TrueNAS app %q entered state %s", app, instance.State)
			}
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: waiting for app %q: %w", ErrAppInstallMayBeInProgress, app, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("%w: app %q did not reach RUNNING within %s; check TrueNAS app lifecycle logs", ErrAppInstallMayBeInProgress, app, waitTimeout)
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
