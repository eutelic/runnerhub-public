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
