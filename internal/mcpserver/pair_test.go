package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDashboardPairingHidesToolsUntilApprovedAndRevokesImmediately(t *testing.T) {
	var mu sync.Mutex
	var credential string
	var approved bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mcp/pair/status":
			state := "unregistered"
			if credential != "" && r.Header.Get("Authorization") == "Bearer "+credential {
				if approved {
					state = "approved"
				} else {
					state = "pending"
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
		case "/api/v1/mcp/pair/request":
			var input struct {
				Name  string `json:"name"`
				Token string `json:"token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Name == "" {
				http.Error(w, "invalid request", 400)
				return
			}
			credential = input.Token
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
		case "/api/v1/hosts":
			if !approved || r.Header.Get("Authorization") != "Bearer "+credential {
				http.Error(w, "forbidden", 403)
				return
			}
			_, _ = w.Write([]byte("[]"))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer api.Close()
	adapter, err := New(Config{BaseURL: api.URL, Pair: true, CredentialsFile: filepath.Join(t.TempDir(), "credentials.json"), Client: api.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := adapter.MCP().Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "paired-test-client"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	getTools := func() map[string]bool {
		t.Helper()
		list, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range list.Tools {
			names[tool.Name] = true
		}
		return names
	}
	names := getTools()
	if len(names) != 1 || !names["request_access"] {
		t.Fatalf("before approval: %v", names)
	}
	callAccess := func() {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "request_access", Arguments: map[string]any{"client_name": "Codex Test"}})
		if err != nil || result.IsError {
			t.Fatalf("request_access error=%v result=%+v", err, result)
		}
	}
	callAccess()
	mu.Lock()
	if credential == "" {
		mu.Unlock()
		t.Fatal("no pairing token created")
	}
	approved = true
	mu.Unlock()
	callAccess()
	names = getTools()
	if !names["runnerhub_list_hosts"] || !names["runnerhub_request_worker"] {
		t.Fatalf("after approval: %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "runnerhub_list_hosts", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("approved tool failed: %+v %v", res, err)
	}
	mu.Lock()
	approved = false
	mu.Unlock()
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "runnerhub_list_hosts", Arguments: map[string]any{}})
	if err == nil && res != nil && !res.IsError {
		t.Fatal("revoked client still accessed tool")
	}
}

func TestPairingRequiresExplicitHTTPOptInForRemoteURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	_, err := New(Config{BaseURL: "http://RUNNERHUB_HOST:8788", Pair: true, CredentialsFile: path})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := New(Config{BaseURL: "http://RUNNERHUB_HOST:8788", Pair: true, AllowHTTP: true, CredentialsFile: path}); err != nil {
		t.Fatalf("trusted LAN opt-in failed: %v", err)
	}
}
