package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewRequiresSecureRemoteConnection(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "remote http", cfg: Config{BaseURL: "http://runnerhub.example"}, wantErr: "require https"},
		{name: "remote without token", cfg: Config{BaseURL: "https://runnerhub.example"}, wantErr: "API_TOKEN is required"},
		{name: "URL contains credentials", cfg: Config{BaseURL: "https://user:pass@runnerhub.example", Token: "key"}, wantErr: "absolute http or https URL"},
		{name: "URL contains query", cfg: Config{BaseURL: "https://runnerhub.example?key=secret", Token: "key"}, wantErr: "absolute http or https URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}

	if _, err := New(Config{BaseURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("local loopback configuration rejected: %v", err)
	}
	if _, err := New(Config{BaseURL: "https://runnerhub.example", Token: "viewer-key"}); err != nil {
		t.Fatalf("HTTPS remote configuration rejected: %v", err)
	}
}

func TestMCPToolsAndWorkerRequest(t *testing.T) {
	var posted bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer operator-key" {
			t.Errorf("Authorization = %q, want configured bearer token", got)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/pools/pool-a/workers" {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		var body struct {
			Repository string `json:"repository"`
			Workflow   string `json:"workflow"`
			Public     bool   `json:"public"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Repository != "acme/app" || body.Workflow != "ci.yml" || body.Public {
			t.Errorf("worker request body = %+v", body)
		}
		posted = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"worker-123","state":"provisioning"}`))
	}))
	defer api.Close()

	adapter, err := New(Config{BaseURL: api.URL, Token: "operator-key", Client: api.Client()})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := adapter.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	gotTools := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		gotTools[tool.Name] = tool
	}
	for _, name := range []string{
		"runnerhub_overview", "runnerhub_list_pools", "runnerhub_list_hosts",
		"runnerhub_list_workers", "runnerhub_get_worker", "runnerhub_diagnose",
		"runnerhub_request_worker",
	} {
		if _, ok := gotTools[name]; !ok {
			t.Errorf("tool %q was not registered", name)
		}
	}
	writeTool := gotTools["runnerhub_request_worker"]
	if writeTool.Annotations == nil || writeTool.Annotations.ReadOnlyHint {
		t.Fatal("worker request must be advertised as a write tool")
	}

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "runnerhub_request_worker",
		Arguments: map[string]any{
			"pool_id": "pool-a", "repository": "acme/app", "workflow": "ci.yml",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("worker request tool returned error: %+v", result.Content)
	}
	if !posted {
		t.Fatal("worker request was not sent to RunnerHub")
	}
	worker, ok := result.StructuredContent.(map[string]any)
	if !ok || worker["id"] != "worker-123" {
		t.Fatalf("worker request result = %#v", result.StructuredContent)
	}
}
