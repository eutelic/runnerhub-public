package deployer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
						if req.Method != "app.get_instance" {
							t.Errorf("unexpected status method %q", req.Method)
							return
						}
						_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"state": "RUNNING"}})
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
