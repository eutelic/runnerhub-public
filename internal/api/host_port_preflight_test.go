package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/deployer"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestDeployerPortPreflightRejectsBeforeBootstrapMutation(t *testing.T) {
	for _, status := range []string{"occupied", "unknown"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			st, err := store.OpenSQLite(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			enrolled, err := hub.Enroll(ctx, "0123456789abcdef", agent.EnrollRequest{Name: "truenas", Provider: "docker"})
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			files := map[string]string{"state.json": "existing state", "config/policy.json": "existing policy", "secrets/api-key": "existing API key", "secrets/workflow-token": "existing token"}
			for name, value := range files {
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			clientCreated := false
			svc := &Service{Store: st, Hub: hub, Auth: auth.New(auth.Config{APIKeys: auth.ParseKeys("admin:admin:operator,viewer:viewer:test")}), DeployerBootstrap: &deployer.BootstrapManager{Root: root, NewClient: func(deployer.TrueNASConfig) (deployer.TrueNASBootstrapClient, error) {
				clientCreated = true
				return nil, errors.New("unexpected TrueNAS access")
			}}}
			hub.SetResultHandler(svc.HandleAgentResult)
			body, _ := json.Marshal(deployer.BootstrapRequest{Endpoint: "https://truenas.invalid", Port: 8790, PortCheckHostID: enrolled.HostID, Username: "service", APIKey: "synthetic-key", HostDataPath: "/mnt/data/runnerhub", RunnerHubApp: "runnerhub", HealthURL: "http://runnerhub.invalid/healthz"})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/deployer/install", bytes.NewReader(body)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer admin")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); svc.Handler().ServeHTTP(response, request) }()
			var command agent.Command
			for command.ID == "" {
				select {
				case <-ctx.Done():
					t.Fatal("port probe command did not arrive")
				default:
				}
				commands := hub.Next(enrolled.HostID)
				if len(commands) > 0 {
					command = commands[0]
					break
				}
				time.Sleep(time.Millisecond)
			}
			if command.Type != agent.CommandHostPortProbe || command.HostPortProbe.Port != 8790 {
				t.Fatalf("unexpected command: %+v", command)
			}
			result := agent.CommandResult{CommandID: command.ID, WorkerID: command.WorkerID, OK: true, HostPortProbe: &agent.HostPortProbeResult{Port: 8790, Status: status}}
			// A different bearer token must not be allowed to certify this host's port.
			raw, _ := json.Marshal(result)
			forged := httptest.NewRequest(http.MethodPost, "/api/v1/agent/results", bytes.NewReader(raw))
			forged.Header.Set("Authorization", "Bearer invalid-agent")
			denied := httptest.NewRecorder()
			svc.Handler().ServeHTTP(denied, forged)
			if denied.Code != http.StatusUnauthorized {
				t.Fatalf("forged result status=%d", denied.Code)
			}
			genuine := httptest.NewRequest(http.MethodPost, "/api/v1/agent/results", bytes.NewReader(raw))
			genuine.Header.Set("Authorization", "Bearer "+enrolled.AgentToken)
			accepted := httptest.NewRecorder()
			svc.Handler().ServeHTTP(accepted, genuine)
			if accepted.Code != http.StatusNoContent {
				t.Fatalf("agent result status=%d: %s", accepted.Code, accepted.Body.String())
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("installation did not finish after probe result")
			}
			expected := http.StatusConflict
			if status == "unknown" {
				expected = http.StatusServiceUnavailable
			}
			if response.Code != expected {
				t.Fatalf("install status=%d: %s", response.Code, response.Body.String())
			}
			if clientCreated {
				t.Fatal("preflight rejection reached TrueNAS bootstrap")
			}
			count := 0
			err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if want, ok := files[relative]; !ok || string(data) != want {
					t.Errorf("bootstrap file changed: %s", relative)
				}
				count++
				return nil
			})
			if err != nil || count != len(files) {
				t.Fatalf("bootstrap inventory changed: files=%d err=%v", count, err)
			}
		})
	}
}
