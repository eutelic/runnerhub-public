// Package mcpserver exposes RunnerHub operations as a small stdio MCP server.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxResponseBytes = 4 << 20

// Config connects the MCP adapter to an existing RunnerHub control plane.
type Config struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

type Server struct {
	baseURL string
	token   string
	client  *http.Client
}

func New(cfg Config) (*Server, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	token := strings.TrimSpace(cfg.Token)
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("RUNNERHUB_API_TOKEN contains invalid whitespace")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("RUNNERHUB_URL must be an absolute http or https URL")
	}
	loopback := isLoopbackHost(u.Hostname())
	if !loopback && u.Scheme != "https" {
		return nil, errors.New("remote RunnerHub connections require https")
	}
	if !loopback && token == "" {
		return nil, errors.New("RUNNERHUB_API_TOKEN is required for remote RunnerHub connections")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Server{baseURL: baseURL, token: token, client: client}, nil
}

func (s *Server) MCP() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "runnerhub", Version: "1.0.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_overview",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "RunnerHub overview"},
		Description: "Read RunnerHub runtime health and compact fleet status. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		result, err := s.getMany(ctx, map[string]string{
			"runtimeHealth": "/api/v1/runtime-health",
			"pools":         "/api/v1/pools",
			"hosts":         "/api/v1/hosts",
			"workers":       "/api/v1/workers?excludeDeleted=true",
			"scaleSets":     "/api/v1/scalesets/status",
			"versions":      "/api/v1/fleet/versions",
		})
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_list_pools",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "List RunnerHub pools"},
		Description: "List configured RunnerHub pools, their GitHub scope, labels, isolation, and concurrency. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		var result any
		if err := s.get(ctx, "/api/v1/pools", &result); err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_list_hosts",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "List RunnerHub hosts"},
		Description: "List RunnerHub hosts and their current state and capacity. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		var result any
		if err := s.get(ctx, "/api/v1/hosts", &result); err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})

	type workerArgs struct {
		State string `json:"state,omitempty" jsonschema:"optional worker state filter"`
		Limit int    `json:"limit,omitempty" jsonschema:"optional history limit from 1 to 100"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_list_workers",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "List RunnerHub workers"},
		Description: "List recent RunnerHub worker history. Optionally filter by state and limit the result. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args workerArgs) (*mcp.CallToolResult, any, error) {
		if args.Limit < 0 || args.Limit > 100 {
			return nil, nil, errors.New("limit must be between 1 and 100")
		}
		query := url.Values{}
		if args.State != "" {
			query.Set("state", args.State)
		}
		if args.Limit > 0 {
			query.Set("limit", fmt.Sprint(args.Limit))
		}
		var result any
		if err := s.get(ctx, "/api/v1/workers/history?"+query.Encode(), &result); err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})

	type getWorkerArgs struct {
		WorkerID string `json:"worker_id" jsonschema:"RunnerHub worker ID"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_get_worker",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "Get RunnerHub worker"},
		Description: "Get one worker's lifecycle, progress, GitHub job association, and any retained startup diagnostics by its RunnerHub worker ID. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getWorkerArgs) (*mcp.CallToolResult, any, error) {
		id := strings.TrimSpace(args.WorkerID)
		if id == "" {
			return nil, nil, errors.New("worker_id is required")
		}
		query := url.Values{}
		query.Set("search", id)
		query.Set("limit", "100")
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := s.get(ctx, "/api/v1/workers/history?"+query.Encode(), &page); err != nil {
			return nil, nil, err
		}
		for _, item := range page.Items {
			if itemID, _ := item["id"].(string); itemID == id {
				return nil, item, nil
			}
		}
		return nil, nil, fmt.Errorf("worker %q was not found", id)
	})

	type diagnosisArgs struct {
		PoolID string `json:"pool_id,omitempty" jsonschema:"optional pool ID to focus the diagnosis on"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_diagnose",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false), Title: "Diagnose RunnerHub"},
		Description: "Gather current runtime health, pool health, scale-set listener status, hosts, workers, and worker progress to help diagnose RunnerHub capacity or startup problems. Returns evidence for the model to explain; it does not assert GitHub queue causes. This tool is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args diagnosisArgs) (*mcp.CallToolResult, any, error) {
		paths := map[string]string{
			"runtimeHealth":  "/api/v1/runtime-health",
			"pools":          "/api/v1/pools",
			"hosts":          "/api/v1/hosts",
			"workers":        "/api/v1/workers?excludeDeleted=true",
			"workerProgress": "/api/v1/workers/progress",
			"scaleSets":      "/api/v1/scalesets/status",
		}
		if args.PoolID != "" {
			paths["poolHealth"] = "/api/v1/pools/" + url.PathEscape(args.PoolID) + "/health"
		}
		result, err := s.getMany(ctx, paths)
		if err != nil {
			return nil, nil, err
		}
		result["limitations"] = []string{"This diagnosis uses RunnerHub's observed state. It does not inspect GitHub's current queued jobs or establish GitHub's root cause."}
		return nil, result, nil
	})

	type requestWorkerArgs struct {
		PoolID     string `json:"pool_id" jsonschema:"pool ID for the existing RunnerHub pool"`
		Repository string `json:"repository,omitempty" jsonschema:"optional owner/repository context for policy and audit"`
		Workflow   string `json:"workflow,omitempty" jsonschema:"optional workflow context for policy and audit"`
		Public     bool   `json:"public,omitempty" jsonschema:"set only when the target workflow is public"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "runnerhub_request_worker",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: false, OpenWorldHint: boolPtr(false), Title: "Request one RunnerHub worker"},
		Description: "Start one ephemeral RunnerHub worker in an existing pool. This changes infrastructure state, may consume host resources, and may accept a queued GitHub Actions job. It does not dispatch a workflow. Ask the user to confirm the pool and repository before calling; RunnerHub still enforces operator authorization, pool security, and policy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args requestWorkerArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.PoolID) == "" {
			return nil, nil, errors.New("pool_id is required")
		}
		body, _ := json.Marshal(map[string]any{"repository": args.Repository, "workflow": args.Workflow, "public": args.Public})
		var result any
		if err := s.request(ctx, http.MethodPost, "/api/v1/pools/"+url.PathEscape(args.PoolID)+"/workers", bytes.NewReader(body), &result); err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})

	return server
}

func (s *Server) get(ctx context.Context, path string, out any) error {
	return s.request(ctx, http.MethodGet, path, nil, out)
}

func (s *Server) getMany(ctx context.Context, paths map[string]string) (map[string]any, error) {
	result := make(map[string]any, len(paths))
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for key, path := range paths {
		key, path := key, path
		wg.Add(1)
		go func() {
			defer wg.Done()
			var value any
			if err := s.get(ctx, path, &value); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("could not read %s: %w", key, err)
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			result[key] = value
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return result, nil
}

func (s *Server) request(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, body)
	if err != nil {
		return err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("RunnerHub request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("could not read RunnerHub response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return errors.New("RunnerHub response exceeded the 4 MiB MCP limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("RunnerHub returned HTTP %d", resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("RunnerHub returned invalid JSON")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func boolPtr(value bool) *bool { return &value }
