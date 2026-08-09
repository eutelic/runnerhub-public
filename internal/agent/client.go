package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/provider"
	"io"
	"net/http"
	"strings"
	"time"
)

type ClientConfig struct {
	ControlPlaneURL string
	EnrollmentToken string
	Name            string
	Provider        string
	AgentVersion    string
	HTTPClient      *http.Client
}
type Client struct {
	cfg       ClientConfig
	http      *http.Client
	providers map[string]provider.Provider
	hostID    string
	token     string
	poll      time.Duration
}

func NewClient(cfg ClientConfig, providers map[string]provider.Provider) (*Client, error) {
	if cfg.ControlPlaneURL == "" || cfg.EnrollmentToken == "" || cfg.Name == "" {
		return nil, errors.New("control plane url, enrollment token and name required")
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: h, providers: providers, poll: 2 * time.Second}, nil
}
func (c *Client) Run(ctx context.Context) error {
	p := c.providers[c.cfg.Provider]
	if p == nil {
		return fmt.Errorf("provider %q not configured", c.cfg.Provider)
	}
	cap, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	var enrolled EnrollResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/enroll", EnrollRequest{Name: c.cfg.Name, Provider: c.cfg.Provider, Capabilities: cap, AgentVersion: c.cfg.AgentVersion}, &enrolled, c.cfg.EnrollmentToken); err != nil {
		return err
	}
	c.hostID, c.token = enrolled.HostID, enrolled.AgentToken
	if enrolled.PollIntervalSeconds > 0 {
		c.poll = time.Duration(enrolled.PollIntervalSeconds) * time.Second
	}
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		if err := c.cycle(ctx, p); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (c *Client) cycle(ctx context.Context, p provider.Provider) error {
	cap, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/heartbeat", Heartbeat{HostID: c.hostID, At: time.Now().UTC(), Capabilities: cap}, nil, c.token); err != nil {
		return err
	}
	var cmds []Command
	if err := c.do(ctx, http.MethodGet, "/api/v1/agent/commands?host_id="+c.hostID, nil, &cmds, c.token); err != nil {
		return err
	}
	for _, cmd := range cmds {
		res := c.execute(ctx, p, cmd)
		_ = c.do(ctx, http.MethodPost, "/api/v1/agent/results", res, nil, c.token)
	}
	return nil
}
func (c *Client) execute(ctx context.Context, p provider.Provider, cmd Command) CommandResult {
	r := CommandResult{CommandID: cmd.ID, WorkerID: cmd.WorkerID, At: time.Now().UTC()}
	switch cmd.Type {
	case CommandCreate:
		if cmd.Spec == nil {
			r.SafeError = "missing worker spec"
			return r
		}
		h, err := p.Create(ctx, *cmd.Spec)
		if err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
		r.Handle = &h
	case CommandInspect:
		if cmd.Handle == nil {
			r.SafeError = "missing worker handle"
			return r
		}
		s, err := p.Inspect(ctx, *cmd.Handle)
		if err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
		r.State = &s
	case CommandStop:
		if cmd.Handle == nil {
			r.SafeError = "missing worker handle"
			return r
		}
		if err := p.Stop(ctx, *cmd.Handle); err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
	case CommandDelete:
		if cmd.Handle == nil {
			r.SafeError = "missing worker handle"
			return r
		}
		if err := p.Delete(ctx, *cmd.Handle); err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
	}
	return r
}
func (c *Client) do(ctx context.Context, method, path string, body any, out any, bearer string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.ControlPlaneURL, "/")+path, rd)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if refreshed := resp.Header.Get("X-RunnerHub-Agent-Token"); refreshed != "" {
		c.token = refreshed
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("control plane returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func safe(err error) string {
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}
