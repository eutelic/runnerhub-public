package deployer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPinCustomComposeReplacesExactlyOneImageAndPreservesOtherFields(t *testing.T) {
	image := "ghcr.io/eutelic/file-ingest:latest"
	config := map[string]any{
		"services": map[string]any{
			"ingest": map[string]any{
				"image": image, "ports": []any{"8789:8789"},
				"environment": map[string]any{"RUN_MODE": "staged"},
			},
			"database": map[string]any{"image": "postgres:17", "volumes": []any{"db:/data"}},
		},
		"volumes": map[string]any{"db": map[string]any{}},
	}
	updated, service, err := pinCustomCompose(config, image, baselineDigest, updatedDigest)
	if err != nil || service != "ingest" {
		t.Fatalf("pin: %s %v", service, err)
	}
	services := updated["services"].(map[string]any)
	ingest := services["ingest"].(map[string]any)
	if got := ingest["image"]; got != "ghcr.io/eutelic/file-ingest@"+updatedDigest {
		t.Fatalf("wrong pinned image: %v", got)
	}
	if got := config["services"].(map[string]any)["ingest"].(map[string]any)["image"]; got != image {
		t.Fatal("mutated source TrueNAS configuration")
	}
	if got := services["database"].(map[string]any)["image"]; got != "postgres:17" {
		t.Fatal("modified unrelated service")
	}
	raw, err := json.Marshal(updated)
	if err != nil || !strings.Contains(string(raw), "RUN_MODE") || !strings.Contains(string(raw), "db") {
		t.Fatalf("lost unrelated configuration: %s %v", raw, err)
	}
	// A subsequent release must use the previously verified pinned image.
	next, _, err := pinCustomCompose(updated, image, updatedDigest, otherDigest)
	if err != nil {
		t.Fatal(err)
	}
	if got := next["services"].(map[string]any)["ingest"].(map[string]any)["image"]; got != "ghcr.io/eutelic/file-ingest@"+otherDigest {
		t.Fatalf("did not advance pinned image: %v", got)
	}
}

func TestPinCustomComposeRejectsUnreviewedMappings(t *testing.T) {
	image := "ghcr.io/eutelic/file-ingest:latest"
	cases := []struct {
		name   string
		config map[string]any
	}{
		{"catalog/wizard or absent compose", map[string]any{"image": image}},
		{"unknown image", map[string]any{"services": map[string]any{"app": map[string]any{"image": "ghcr.io/eutelic/file-ingest:other"}}}},
		{"two matching services", map[string]any{"services": map[string]any{
			"a": map[string]any{"image": image}, "b": map[string]any{"image": image},
		}}},
		{"include directive", map[string]any{"include": "other.yml", "services": map[string]any{"app": map[string]any{"image": image}}}},
		{"build directive", map[string]any{"services": map[string]any{"app": map[string]any{"image": image, "build": "."}}}},
		{"wrong pinned baseline", map[string]any{"services": map[string]any{"app": map[string]any{"image": "ghcr.io/eutelic/file-ingest@" + otherDigest}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := pinCustomCompose(tc.config, image, baselineDigest, updatedDigest); err == nil {
				t.Fatal("unsafe TrueNAS Compose mapping accepted")
			}
		})
	}
}

func TestTrueNASPinnedRedeployUsesAppUpdateAndVerifiesRunningImage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	image := "ghcr.io/eutelic/file-ingest:latest"
	pinned := "ghcr.io/eutelic/file-ingest@" + updatedDigest
	var updated bool
	var updatedConfig map[string]any
	var callSequence []string
	var callsMu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
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
			callsMu.Lock()
			callSequence = append(callSequence, req.Method)
			callsMu.Unlock()
			switch req.Method {
			case "auth.login_ex":
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response_type": "SUCCESS"}})
			case "app.get_instance":
				config := map[string]any{"services": map[string]any{"ingest": map[string]any{"image": image, "volumes": []any{"/mnt/main:/data"}}}}
				active := []string{image}
				if updated {
					config = updatedConfig
					active = []string{pinned}
				}
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
					"name": "file-ingest", "custom_app": true, "state": "RUNNING", "config": config,
					"active_workloads": map[string]any{"images": active},
				}})
			case "app.update":
				var params []json.RawMessage
				if err := json.Unmarshal(req.Params, &params); err != nil || len(params) != 2 {
					t.Errorf("unexpected update params: %s %v", req.Params, err)
					return
				}
				var update struct {
					CustomComposeConfig map[string]any `json:"custom_compose_config"`
				}
				if err := json.Unmarshal(params[1], &update); err != nil {
					t.Errorf("decode update: %v", err)
					return
				}
				updatedConfig = update.CustomComposeConfig
				if got := updatedConfig["services"].(map[string]any)["ingest"].(map[string]any)["image"]; got != pinned {
					t.Errorf("app.update did not pin image: %v", got)
					return
				}
				updated = true
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"state": "RUNNING"}})
			default:
				t.Errorf("unexpected TrueNAS method %s", req.Method)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.RedeployPinnedAndWait(ctx, "file-ingest", image, baselineDigest, updatedDigest); err != nil {
		t.Fatal(err)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if strings.Join(callSequence, ",") != "auth.login_ex,app.get_instance,app.get_instance,app.update,app.get_instance" {
		t.Fatalf("unexpected TrueNAS RPC sequence: %v", callSequence)
	}
}

func TestTrueNASPinnedRedeployRefusesConcurrentComposeEdit(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var reads int
	var updateCalled bool
	var stateMu sync.Mutex
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
			switch req.Method {
			case "auth.login_ex":
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response_type": "SUCCESS"}})
			case "app.get_instance":
				stateMu.Lock()
				reads++
				currentRead := reads
				stateMu.Unlock()
				mode := "original"
				if currentRead > 1 {
					mode = "operator-edit"
				}
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
					"name": "file-ingest", "custom_app": true, "state": "RUNNING",
					"config": map[string]any{"services": map[string]any{"ingest": map[string]any{
						"image":       "ghcr.io/eutelic/file-ingest:latest",
						"environment": map[string]any{"MODE": mode},
					}}},
				}})
			case "app.update":
				stateMu.Lock()
				updateCalled = true
				stateMu.Unlock()
				t.Error("updated TrueNAS despite concurrent Compose edit")
				return
			default:
				t.Errorf("unexpected TrueNAS method: %s", req.Method)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.RedeployPinnedAndWait(ctx, "file-ingest", "ghcr.io/eutelic/file-ingest:latest", baselineDigest, updatedDigest); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("concurrent Compose mutation was not rejected: %v", err)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if updateCalled || reads != 2 {
		t.Fatalf("unexpected TrueNAS mutation/read count: update=%t reads=%d", updateCalled, reads)
	}
}
