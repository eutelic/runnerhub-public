package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestReplaceRunnerHubControlImagePinsExactlyOneAnchor(t *testing.T) {
	compose := "services:\n  control: &control-image ghcr.io/eutelic/runnerhub-control:latest\n  deployer:\n    image: ghcr.io/eutelic/runnerhub-deployer:latest\n"
	image := "ghcr.io/eutelic/runnerhub-control@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got, err := replaceRunnerHubControlImage(compose, image)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "control: &control-image "+image) {
		t.Fatalf("updated compose did not pin the control image:\n%s", got)
	}
	if strings.Contains(got, "runnerhub-deployer@") {
		t.Fatalf("updated non-control image unexpectedly changed:\n%s", got)
	}
	if _, err := replaceRunnerHubControlImage("services:\n  control:\n    image: ghcr.io/eutelic/runnerhub-control:latest\n", image); err == nil {
		t.Fatal("expected unsupported Compose shape to fail closed")
	}
}

func TestTrueNASRedeployWaitsForJobResponse(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		for call := 0; call < 3; call++ {
			var req struct {
				ID     uint64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read request: %v", err)
				return
			}
			switch req.Method {
			case "auth.login_ex":
				var params []map[string]any
				if err := json.Unmarshal(req.Params, &params); err != nil {
					t.Errorf("decode auth params: %v", err)
					return
				}
				if len(params) != 1 || params[0]["mechanism"] != "API_KEY_PLAIN" ||
					params[0]["username"] != "deployer" || params[0]["api_key"] != "secret" {
					t.Errorf("unexpected auth params: %s", req.Params)
					return
				}
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  map[string]any{"response_type": "SUCCESS", "user_info": nil},
				})
			case "app.pull_images":
				// TrueNAS job methods emit collection updates before the
				// final method response. The client must ignore this event,
				// not mistake it for deployment completion.
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0",
					"method":  "collection_update",
					"params": map[string]any{
						"msg":        "added",
						"collection": "core.get_jobs",
						"fields":     map[string]any{"id": 42, "message_ids": []uint64{req.ID}},
					},
				})
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
			case "app.get_instance":
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  map[string]any{"state": "RUNNING"},
				})
			default:
				t.Errorf("unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	endpoint := "wss" + strings.TrimPrefix(server.URL, "https")
	client, err := NewTrueNASClient(TrueNASConfig{
		Endpoint:           endpoint,
		Username:           "deployer",
		APIKey:             "secret",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.RedeployAndWait(ctx, "sample-app"); err != nil {
		t.Fatal(err)
	}
}

func TestTrueNASImageRedeployUpdatesAndVerifiesDigest(t *testing.T) {
	image := "ghcr.io/eutelic/runnerhub-control@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	up := websocket.Upgrader{}
	applied := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for i := 0; i < 5; i++ {
			var req struct {
				ID     uint64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read: %v", err)
				return
			}
			var result any
			switch req.Method {
			case "auth.login_ex":
				result = map[string]any{"response_type": "SUCCESS"}
			case "app.get_instance":
				if !strings.Contains(string(req.Params), "retrieve_config") {
					t.Errorf("expected retrieve_config option: %s", req.Params)
				}
				current := "ghcr.io/eutelic/runnerhub-control:latest"
				if applied {
					current = image
				}
				var containerDetails []any
				if applied {
					containerDetails = []any{map[string]any{"service_name": "control", "image": image}}
				}
				result = map[string]any{
					"name": "runnerhub", "id": "runnerhub", "state": "RUNNING", "custom_app": true,
					"config":           map[string]any{"services": map[string]any{"control": map[string]any{"image": current, "ports": []any{"8788:8080"}}, "agent": map[string]any{"image": "ghcr.io/eutelic/runnerhub-agent:latest"}}},
					"active_workloads": map[string]any{"container_details": containerDetails},
				}
			case "app.update":
				if !strings.Contains(string(req.Params), image) || !strings.Contains(string(req.Params), "custom_compose_config") {
					t.Errorf("pinned config update not submitted: %s", req.Params)
				}
				applied = true
				result = map[string]any{"state": "RUNNING"}
			default:
				t.Errorf("unexpected method %q", req.Method)
				return
			}
			if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewTrueNASClient(TrueNASConfig{Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"), Username: "deployer", APIKey: "secret", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.RedeployImageAndWait(ctx, "runnerhub", image); err != nil {
		t.Fatal(err)
	}
}

func TestPinRunnerHubComposeConfigPreservesOtherServices(t *testing.T) {
	image := "ghcr.io/eutelic/runnerhub-control@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	before := map[string]any{"services": map[string]any{
		"control": map[string]any{"image": "ghcr.io/eutelic/runnerhub-control:latest", "volumes": []any{"/mnt/data:/data"}},
		"agent":   map[string]any{"image": "ghcr.io/eutelic/runnerhub-agent:latest"},
	}, "x-runnerhub-images": map[string]any{"control": "ghcr.io/eutelic/runnerhub-control:latest"}}
	updated, err := pinRunnerHubComposeConfig(before, image)
	if err != nil {
		t.Fatal(err)
	}
	services := updated["services"].(map[string]any)
	if services["control"].(map[string]any)["image"] != image {
		t.Fatal("control image not pinned")
	}
	if services["agent"].(map[string]any)["image"] != "ghcr.io/eutelic/runnerhub-agent:latest" {
		t.Fatal("unrelated service changed")
	}
	if before["services"].(map[string]any)["control"].(map[string]any)["image"] == image {
		t.Fatal("mutated the original config")
	}
	if updated["x-runnerhub-images"].(map[string]any)["control"] != image {
		t.Fatal("control extension not pinned")
	}
}

func TestTrueNASFindAppMountPrefersControlDataBind(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for call := 0; call < 2; call++ {
			var req struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read request: %v", err)
				return
			}
			switch req.Method {
			case "auth.login_ex":
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response_type": "SUCCESS"}})
			case "app.get_instance":
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"active_workloads": map[string]any{
							"container_details": []map[string]any{
								{"service_name": "agent", "volume_mounts": []map[string]any{{"source": "/mnt/tank/runnerhub/agent", "destination": "/data", "type": "bind"}}},
								{"service_name": "control", "volume_mounts": []map[string]any{{"source": "/mnt/tank/runnerhub/data", "destination": "/data", "type": "bind"}}},
							},
						},
					},
				})
			default:
				t.Errorf("unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	endpoint := "wss" + strings.TrimPrefix(server.URL, "https")
	client, err := NewTrueNASClient(TrueNASConfig{
		Endpoint:           endpoint,
		Username:           "deployer",
		APIKey:             "secret",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mount, err := client.FindAppMount(ctx, "runnerhub", "/data")
	if err != nil {
		t.Fatal(err)
	}
	if mount != "/mnt/tank/runnerhub/data" {
		t.Fatalf("got %q, want control data mount", mount)
	}
}

func TestTrueNASEnsureCustomAppCreatesAndUpdates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		existing   bool
		wantMethod string
	}{
		{name: "create", existing: false, wantMethod: "app.create"},
		{name: "update", existing: true, wantMethod: "app.update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade: %v", err)
					return
				}
				defer conn.Close()

				for call := 0; call < 4; call++ {
					var req struct {
						ID     uint64          `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if err := conn.ReadJSON(&req); err != nil {
						t.Errorf("read request: %v", err)
						return
					}
					switch call {
					case 0:
						if req.Method != "auth.login_ex" {
							t.Errorf("unexpected auth method %q", req.Method)
							return
						}
						_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response_type": "SUCCESS"}})
					case 1:
						if req.Method != "app.query" {
							t.Errorf("unexpected query method %q", req.Method)
							return
						}
						apps := []map[string]any{}
						if tc.existing {
							apps = append(apps, map[string]any{"name": "runnerhub-deployer", "id": "runnerhub-deployer", "custom_app": true, "state": "RUNNING"})
						}
						_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": apps})
					case 2:
						if req.Method != tc.wantMethod {
							t.Errorf("got %q, want %q", req.Method, tc.wantMethod)
							return
						}
						if !strings.Contains(string(req.Params), "custom_compose_config_string") || !strings.Contains(string(req.Params), "services:") {
							t.Errorf("custom Compose missing from request: %s", req.Params)
							return
						}
						_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
					case 3:
						if req.Method != "app.query" {
							t.Errorf("unexpected status method %q", req.Method)
							return
						}
						_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []map[string]any{{"name": "runnerhub-deployer", "id": "runnerhub-deployer", "state": "RUNNING"}}})
					}
				}
			}))
			defer server.Close()

			endpoint := "wss" + strings.TrimPrefix(server.URL, "https")
			client, err := NewTrueNASClient(TrueNASConfig{
				Endpoint:           endpoint,
				Username:           "deployer",
				APIKey:             "secret",
				InsecureSkipVerify: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.EnsureCustomApp(ctx, "runnerhub-deployer", "services:\n  deployer: {}"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTrueNASClientRejectsInsecureWebSocket(t *testing.T) {
	_, err := NewTrueNASClient(TrueNASConfig{
		Endpoint: "ws://runnerhub.example.test/api/current",
		Username: "deployer",
		APIKey:   "secret",
	})
	if err == nil {
		t.Fatal("expected insecure WebSocket endpoint to be rejected")
	}
}

func TestTrueNASInstallWaitsForDelayedAppRegistration(t *testing.T) {
	var queries atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			var result any
			switch req.Method {
			case "auth.login_ex":
				result = map[string]any{"response_type": "SUCCESS"}
			case "app.query":
				n := queries.Add(1)
				switch n {
				case 1, 2: // discovery and first poll: app absent
					result = []any{}
				case 3:
					result = []map[string]any{{"name": "runnerhub-deployer", "state": "DEPLOYING"}}
				default:
					result = []map[string]any{{"name": "runnerhub-deployer", "state": "RUNNING"}}
				}
			case "app.create":
				result = nil
			default:
				t.Errorf("unexpected method %s", req.Method)
				return
			}
			if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewTrueNASClient(TrueNASConfig{
		Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"),
		Username: "deployer", APIKey: "secret", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.appPollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.EnsureCustomApp(ctx, "runnerhub-deployer", "services:\n  deployer: {}"); err != nil {
		t.Fatal(err)
	}
	if queries.Load() < 4 {
		t.Fatalf("returned success before app reached RUNNING: %d queries", queries.Load())
	}
}

func TestTrueNASInstallTimeoutRetainsPendingClassification(t *testing.T) {
	var queries atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			var result any
			switch req.Method {
			case "auth.login_ex":
				result = map[string]any{"response_type": "SUCCESS"}
			case "app.query":
				queries.Add(1)
				result = []any{} // TrueNAS has not yet registered its async app
			case "app.create":
				result = nil
			default:
				t.Errorf("unexpected method: %s", req.Method)
				return
			}
			if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewTrueNASClient(TrueNASConfig{
		Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"),
		Username: "deployer", APIKey: "secret", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.appPollInterval = time.Millisecond
	client.appWaitTimeout = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = client.EnsureCustomApp(ctx, "runnerhub-deployer", "services:\n  deployer: {}")
	if !errors.Is(err, ErrAppInstallMayBeInProgress) {
		t.Fatalf("expected pending-install error on timeout, got %v", err)
	}
	if queries.Load() < 2 {
		t.Fatal("installation did not poll for readiness before timing out")
	}
}

func TestTrueNASImmutableImagePreflightRequiresFullAdmin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles []string
		allow bool
	}{
		{"apps-write-only", []string{"APPS_READ", "APPS_WRITE"}, false},
		{"full-admin", []string{"FULL_ADMIN"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := websocket.Upgrader{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := up.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				for {
					var req struct {
						ID     uint64 `json:"id"`
						Method string `json:"method"`
					}
					if err := conn.ReadJSON(&req); err != nil {
						return
					}
					var out any
					switch req.Method {
					case "auth.login_ex":
						out = map[string]any{"response_type": "SUCCESS"}
					case "auth.me":
						out = map[string]any{"roles": tc.roles}
					case "app.get_instance":
						out = map[string]any{"id": "runnerhub", "name": "runnerhub", "state": "RUNNING", "custom_app": true,
							"config": map[string]any{"services": map[string]any{
								"control": map[string]any{"image": "ghcr.io/eutelic/runnerhub-control:latest"},
							}},
						}
					default:
						t.Errorf("preflight made unexpected mutation or API call %q", req.Method)
						return
					}
					_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": out})
				}
			}))
			defer server.Close()
			client, err := NewTrueNASClient(TrueNASConfig{Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"), Username: "deployer", APIKey: "secret", InsecureSkipVerify: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			image := "ghcr.io/eutelic/runnerhub-control@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			err = client.VerifyImmutableImageUpdate(ctx, "runnerhub", image)
			if tc.allow && err != nil {
				t.Fatal(err)
			}
			if !tc.allow && (err == nil || !strings.Contains(err.Error(), "FULL_ADMIN")) {
				t.Fatalf("expected role denial before pause; got %v", err)
			}
		})
	}
}

func TestTrueNASListAppsIncludesImagesForPublishingSafety(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			var req struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read: %v", err)
				return
			}
			switch req.Method {
			case "auth.login_ex":
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response_type": "SUCCESS"}})
			case "app.query":
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"result": []map[string]any{
						{"name": "file-ingest", "state": "RUNNING", "active_workloads": map[string]any{
							"images": []string{"ghcr.io/eutelic/file-ingest:latest", "redis:7"},
						}},
					},
				})
			default:
				t.Errorf("unexpected request: %s", req.Method)
			}
		}
	}))
	defer server.Close()
	client, err := NewTrueNASClient(TrueNASConfig{
		Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"),
		Username: "deployer", APIKey: "secret", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	apps, err := client.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Name != "file-ingest" || len(apps[0].Images) != 2 ||
		apps[0].Images[0] != "ghcr.io/eutelic/file-ingest:latest" {
		t.Fatalf("unexpected TrueNAS inventory: %+v", apps)
	}
}

func TestTrueNASInstallJobFailureReportsSafePortConflict(t *testing.T) {
	for _, tc := range []struct {
		name             string
		jobError         string
		wantPortConflict bool
	}{
		{
			name:             "loopback Docker port binding",
			jobError:         "driver failed programming external connectivity: Bind for 127.0.0.1:8790 failed: port is already allocated; SECRET_TOKEN=do-not-print",
			wantPortConflict: true,
		},
		{
			name:             "unrelated failed job",
			jobError:         "compose failed due to SECRET_TOKEN=do-not-print",
			wantPortConflict: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("websocket upgrade: %v", err)
					return
				}
				defer conn.Close()
				for {
					var req struct {
						ID     uint64          `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if err := conn.ReadJSON(&req); err != nil {
						return
					}
					var result any
					switch req.Method {
					case "auth.login_ex":
						result = map[string]any{"response_type": "SUCCESS"}
					case "app.query":
						result = []any{}
					case "app.create":
						// This unrelated notification must not supply the job ID.
						_ = conn.WriteJSON(map[string]any{
							"jsonrpc": "2.0", "method": "collection_update",
							"params": map[string]any{
								"collection": "core.get_jobs", "msg": "added",
								"fields": map[string]any{"id": 999, "message_ids": []uint64{req.ID + 1}},
							},
						})
						_ = conn.WriteJSON(map[string]any{
							"jsonrpc": "2.0", "method": "collection_update",
							"params": map[string]any{
								"collection": "core.get_jobs", "msg": "added",
								"fields": map[string]any{"id": 777, "message_ids": []uint64{req.ID}},
							},
						})
						result = nil
					case "core.get_jobs":
						if !strings.Contains(string(req.Params), "777") {
							t.Errorf("job lookup used wrong job ID: %s", req.Params)
						}
						result = []map[string]any{{"id": 777, "state": "FAILED", "error": tc.jobError}}
					default:
						t.Errorf("unexpected TrueNAS method %q", req.Method)
						return
					}
					if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			client, err := NewTrueNASClient(TrueNASConfig{
				Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"),
				Username: "deployer", APIKey: "secret", InsecureSkipVerify: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.appPollInterval = time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = client.EnsureCustomApp(ctx, "runnerhub-deployer", "services:\n  deployer: {}")
			if err == nil {
				t.Fatal("expected terminal job failure")
			}
			if errors.Is(err, ErrAppPortConflict) != tc.wantPortConflict {
				t.Fatalf("unexpected port conflict classification: %v", err)
			}
			if strings.Contains(err.Error(), "SECRET_TOKEN") || strings.Contains(err.Error(), "do-not-print") {
				t.Fatalf("raw TrueNAS job error leaked: %v", err)
			}
		})
	}
}

func TestTrueNASPortBindConflictDoesNotMisclassifyGenericErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"failed to bind: address already in use", true},
		{"port has already been allocated", true},
		{"authentication failed", false},
		{"job is still running", false},
	} {
		if got := trueNASPortBindConflict(tc.message); got != tc.want {
			t.Errorf("conflict(%q)=%v, want %v", tc.message, got, tc.want)
		}
	}
}

func TestTrueNASExistingAppIsNotReadyUntilInstallJobFinishes(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{name: "update job failed while old app still RUNNING", fail: true},
		{name: "update job pending before succeeding", fail: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lookups atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				for {
					var req struct {
						ID     uint64 `json:"id"`
						Method string `json:"method"`
					}
					if err := conn.ReadJSON(&req); err != nil {
						return
					}
					var result any
					switch req.Method {
					case "auth.login_ex":
						result = map[string]any{"response_type": "SUCCESS"}
					case "app.query":
						result = []map[string]any{{"id": "runnerhub-deployer", "name": "runnerhub-deployer", "custom_app": true, "state": "RUNNING"}}
					case "app.update":
						_ = conn.WriteJSON(map[string]any{
							"jsonrpc": "2.0", "method": "collection_update",
							"params": map[string]any{
								"collection": "core.get_jobs",
								"fields":     map[string]any{"id": 424, "message_ids": []uint64{req.ID}},
							},
						})
					case "core.get_jobs":
						n := lookups.Add(1)
						state := "SUCCESS"
						if tc.fail || n == 1 {
							state = "RUNNING"
						}
						if tc.fail {
							state = "FAILED"
						}
						result = []map[string]any{{"id": 424, "state": state, "error": "Bind for 127.0.0.1:8790 failed: port is already allocated"}}
					default:
						t.Errorf("unexpected method %s", req.Method)
						return
					}
					if err := conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			client, err := NewTrueNASClient(TrueNASConfig{
				Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"),
				Username: "deployer", APIKey: "secret", InsecureSkipVerify: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.appPollInterval = time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = client.EnsureCustomApp(ctx, "runnerhub-deployer", "services: {}")
			if tc.fail {
				if !errors.Is(err, ErrAppPortConflict) {
					t.Fatalf("old RUNNING app masked failed update: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !tc.fail && lookups.Load() < 2 {
				t.Fatalf("returned ready before update job succeeded: checked %d times", lookups.Load())
			}
		})
	}
}
