package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/provider"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AgentUpdater interface {
	Launch(context.Context, AgentUpdateSpec) error
}
type ClientConfig struct {
	ControlPlaneURL   string
	EnrollmentToken   string
	CredentialFile    string
	Name              string
	Provider          string
	AgentVersion      string
	PlacementDomainID string
	HTTPClient        *http.Client
	AllowInsecureHTTP bool
	Updater           AgentUpdater
}
type storedCredential struct {
	HostID string `json:"hostId"`
	Token  string `json:"token"`
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
	if cfg.ControlPlaneURL == "" || cfg.Name == "" {
		return nil, errors.New("control plane url and name required")
	}
	u, err := url.Parse(cfg.ControlPlaneURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("control plane url must contain scheme, host, and optional path")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (cfg.AllowInsecureHTTP || loopbackHost(u.Hostname()))) {
		return nil, errors.New("control plane url must use https; set AllowInsecureHTTP only on a separately protected network")
	}
	cfg.ControlPlaneURL = strings.TrimRight(cfg.ControlPlaneURL, "/")
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	c := &Client{cfg: cfg, http: h, providers: providers, poll: 2 * time.Second}
	if cfg.CredentialFile != "" {
		cred, err := loadCredential(cfg.CredentialFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load agent credential: %w", err)
		}
		if err == nil {
			c.hostID, c.token = cred.HostID, cred.Token
		}
	}
	if (c.hostID == "" || c.token == "") && strings.TrimSpace(cfg.EnrollmentToken) == "" {
		return nil, errors.New("enrollment token is required until a stored agent credential exists")
	}
	return c, nil
}
func (c *Client) Run(ctx context.Context) error {
	p := c.providers[c.cfg.Provider]
	if p == nil {
		return fmt.Errorf("provider %q not configured", c.cfg.Provider)
	}
	backoff := time.Second
	for {
		if c.hostID == "" || c.token == "" {
			if strings.TrimSpace(c.cfg.EnrollmentToken) == "" {
				return errors.New("agent credential unavailable or rejected; provide RUNNERHUB_ENROLLMENT_TOKEN to re-enroll")
			}
			if err := c.enroll(ctx, p); err != nil {
				log.Printf("agent enrollment attempt failed: %v; retrying in %s", err, backoff)
				if err := waitBackoff(ctx, backoff); err != nil {
					return err
				}
				backoff = minDuration(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
		}
		if err := c.cycle(ctx, p); err != nil && !errors.Is(err, context.Canceled) {
			if strings.Contains(err.Error(), "returned 401") {
				c.hostID, c.token = "", ""
				if strings.TrimSpace(c.cfg.EnrollmentToken) == "" {
					return errors.New("stored agent credential was rejected; provide RUNNERHUB_ENROLLMENT_TOKEN to re-enroll")
				}
			}
			if err := waitBackoff(ctx, backoff); err != nil {
				return err
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.poll):
		}
	}
}
func (c *Client) enroll(ctx context.Context, p provider.Provider) error {
	cap, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	var enrolled EnrollResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/enroll", EnrollRequest{Name: c.cfg.Name, Provider: c.cfg.Provider, Capabilities: cap, AgentVersion: c.cfg.AgentVersion, PlacementDomainID: c.cfg.PlacementDomainID}, &enrolled, c.cfg.EnrollmentToken); err != nil {
		return err
	}
	c.hostID, c.token = enrolled.HostID, enrolled.AgentToken
	if c.hostID == "" || c.token == "" {
		return errors.New("control plane returned incomplete agent credentials")
	}
	if err := c.saveCredential(); err != nil {
		return fmt.Errorf("persist enrolled agent credential: %w", err)
	}
	if enrolled.PollIntervalSeconds > 0 {
		c.poll = time.Duration(enrolled.PollIntervalSeconds) * time.Second
	}
	return nil
}
func (c *Client) cycle(ctx context.Context, p provider.Provider) error {
	cap, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/heartbeat", Heartbeat{HostID: c.hostID, At: time.Now().UTC(), AgentVersion: c.cfg.AgentVersion, Capabilities: cap}, nil, c.token); err != nil {
		return err
	}
	var cmds []Command
	if err := c.do(ctx, http.MethodGet, "/api/v1/agent/commands?host_id="+url.QueryEscape(c.hostID), nil, &cmds, c.token); err != nil {
		return err
	}
	for _, cmd := range cmds {
		res := c.execute(ctx, p, cmd)
		if err := c.postResult(ctx, res); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) postResult(ctx context.Context, res CommandResult) error {
	delay := 250 * time.Millisecond
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		if err := c.do(ctx, http.MethodPost, "/api/v1/agent/results", res, nil, c.token); err == nil {
			return nil
		} else {
			last = err
		}
		if err := waitBackoff(ctx, delay); err != nil {
			return err
		}
		delay = minDuration(delay*2, 4*time.Second)
	}
	return last
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
		started := time.Now()
		err := p.Delete(ctx, *cmd.Handle)
		r.ProviderDeleteDurationNanos = time.Since(started).Nanoseconds()
		if err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
	case CommandUpdateAgent:
		if c.cfg.Provider != "docker" || cmd.AgentUpdate == nil || cmd.AgentUpdate.HostID != c.hostID || cmd.AgentUpdate.OperationID == "" || cmd.AgentUpdate.OperationID != cmd.WorkerID {
			r.SafeError = "agent update command does not match enrolled Docker host and operation"
			return r
		}
		if c.cfg.Updater == nil {
			r.SafeError = "detached Docker updater unavailable on this host"
			return r
		}
		if err := c.cfg.Updater.Launch(ctx, *cmd.AgentUpdate); err != nil {
			r.SafeError = safe(err)
			return r
		}
		r.OK = true
	default:
		r.SafeError = "unsupported command type"
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
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.ControlPlaneURL+path, rd)
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
	if refreshed := strings.TrimSpace(resp.Header.Get("X-RunnerHub-Agent-Token")); refreshed != "" && refreshed != c.token {
		old := c.token
		c.token = refreshed
		if err := c.saveCredential(); err != nil {
			c.token = old
			return fmt.Errorf("persist refreshed agent credential: %w", err)
		}
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
func (c *Client) saveCredential() error {
	if c.cfg.CredentialFile == "" {
		return nil
	}
	return writeCredential(c.cfg.CredentialFile, storedCredential{HostID: c.hostID, Token: c.token})
}
func loadCredential(path string) (storedCredential, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return storedCredential{}, err
	}
	var c storedCredential
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.HostID) == "" || strings.TrimSpace(c.Token) == "" {
		return c, errors.New("stored agent credential is incomplete")
	}
	return c, nil
}
func writeCredential(path string, c storedCredential) error {
	if strings.TrimSpace(c.HostID) == "" || strings.TrimSpace(c.Token) == "" {
		return errors.New("agent credential is incomplete")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".runnerhub-agent-credential-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func safe(err error) string {
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}
func waitBackoff(ctx context.Context, d time.Duration) error {
	maxJitter := maxDuration(d/4, time.Millisecond)
	jitter := time.Duration(0)
	if n, err := rand.Int(rand.Reader, big.NewInt(int64(maxJitter))); err == nil {
		jitter = time.Duration(n.Int64())
	}
	t := time.NewTimer(d + jitter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
