package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errNotFound = errors.New("proxmox resource not found")

type Config struct {
	BaseURL      string
	TokenID      string
	TokenSecret  string
	Node         string
	TemplateVMID int
	HostID       string
	HTTPClient   *http.Client
	SnippetDir   string
	SnippetStore string
}
type Provider struct {
	cfg    Config
	client *http.Client
}

func New(c Config) (*Provider, error) {
	if c.BaseURL == "" || c.TokenID == "" || c.TokenSecret == "" || c.Node == "" || c.TemplateVMID <= 0 {
		return nil, errors.New("proxmox base url, token, node and template vmid required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("proxmox base url must use https")
	}
	h := c.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	if c.SnippetStore == "" {
		c.SnippetStore = "local"
	}
	return &Provider{cfg: c, client: h}, nil
}
func (p *Provider) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	var out struct {
		Data struct {
			MaxCPU int64 `json:"maxcpu"`
			MaxMem int64 `json:"maxmem"`
			RootFS struct {
				Avail int64 `json:"avail"`
			} `json:"rootfs"`
		} `json:"data"`
	}
	if err := p.form(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/status", url.PathEscape(p.cfg.Node)), nil, &out); err != nil {
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{Provider: "proxmox", OS: "linux", Architecture: "amd64", Capacity: core.Resources{CPUMillis: out.Data.MaxCPU * 1000, MemoryBytes: out.Data.MaxMem, DiskBytes: out.Data.RootFS.Avail}, IsolationModes: []core.IsolationMode{core.IsolationVMProvider}, Features: map[string]bool{"clone": true, "cloudInit": true, "ephemeralDisk": true, "taskWait": true}, Labels: map[string]string{"isolation.vm-provider": "true"}}, nil
}
func (p *Provider) Validate(_ context.Context, s provider.WorkerSpec) error {
	if strings.TrimSpace(s.WorkerID) == "" || strings.TrimSpace(s.RunnerName) == "" {
		return errors.New("worker id and runner name required")
	}
	if s.Isolation != core.IsolationVMProvider {
		return errors.New("proxmox provider requires vm-provider isolation")
	}
	if s.JITConfig == "" {
		return errors.New("jit config required")
	}
	return nil
}
func (p *Provider) Create(ctx context.Context, s provider.WorkerSpec) (provider.WorkerHandle, error) {
	if err := p.Validate(ctx, s); err != nil {
		return provider.WorkerHandle{}, err
	}
	key := safeName(s.WorkerID)
	if key == "" {
		return provider.WorkerHandle{}, errors.New("invalid worker id")
	}
	vmid := 100000 + hashID(s.WorkerID)%800000
	name := vmName(key)
	created := false
	exists, err := p.ownedVM(ctx, vmid, name)
	if err != nil {
		return provider.WorkerHandle{}, err
	}
	if !exists {
		form := url.Values{"newid": {strconv.Itoa(vmid)}, "name": {name}, "full": {"1"}}
		if err := p.task(ctx, http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone", url.PathEscape(p.cfg.Node), p.cfg.TemplateVMID), form); err != nil {
			return provider.WorkerHandle{}, err
		}
		created = true
	}
	if p.cfg.SnippetDir == "" {
		if created {
			_ = p.task(context.Background(), http.MethodDelete, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", url.PathEscape(p.cfg.Node), vmid), nil)
		}
		return provider.WorkerHandle{}, errors.New("proxmox snippet directory is required for JIT bootstrap")
	}
	snippetName := "runnerhub-" + key + ".yaml"
	if err := p.writeSnippet(snippetName, s.JITConfig); err != nil {
		return provider.WorkerHandle{}, err
	}
	ci := url.Values{"ciuser": {"runner"}, "description": {"RunnerHub worker " + s.WorkerID}, "cicustom": {"user=" + p.cfg.SnippetStore + ":snippets/" + snippetName}}
	if s.Resources.CPUMillis > 0 {
		ci.Set("cores", strconv.Itoa(max(1, int((s.Resources.CPUMillis+999)/1000))))
	}
	if s.Resources.MemoryBytes > 0 {
		ci.Set("memory", strconv.FormatInt(max(512, s.Resources.MemoryBytes/(1024*1024)), 10))
	}
	if err := p.form(ctx, http.MethodPut, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(p.cfg.Node), vmid), ci, nil); err != nil {
		if created {
			p.cleanupPartial(vmid, key)
		}
		return provider.WorkerHandle{}, err
	}
	if err := p.task(ctx, http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", url.PathEscape(p.cfg.Node), vmid), nil); err != nil {
		if created {
			p.cleanupPartial(vmid, key)
		}
		return provider.WorkerHandle{}, err
	}
	return provider.WorkerHandle{ProviderID: strconv.Itoa(vmid) + "@" + key, HostID: p.cfg.HostID}, nil
}
func (p *Provider) ownedVM(ctx context.Context, vmid int, expectedName string) (bool, error) {
	var out struct {
		Data struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"data"`
	}
	err := p.form(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(p.cfg.Node), vmid), nil, &out)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if out.Data.Name != expectedName {
		return false, fmt.Errorf("proxmox vmid %d is already owned by %q", vmid, out.Data.Name)
	}
	return true, nil
}
func (p *Provider) cleanupPartial(vmid int, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = p.task(ctx, http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/stop", url.PathEscape(p.cfg.Node), vmid), nil)
	_ = p.task(ctx, http.MethodDelete, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", url.PathEscape(p.cfg.Node), vmid), nil)
	if p.cfg.SnippetDir != "" {
		_ = os.Remove(filepath.Join(p.cfg.SnippetDir, "runnerhub-"+key+".yaml"))
	}
}
func (p *Provider) writeSnippet(name, jit string) error {
	if err := os.MkdirAll(p.cfg.SnippetDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(p.cfg.SnippetDir, name)
	if filepath.Dir(path) != filepath.Clean(p.cfg.SnippetDir) {
		return errors.New("invalid proxmox snippet path")
	}
	encoded := jsonString(jit)
	content := "#cloud-config\nwrite_files:\n  - path: /run/runnerhub-jit\n    permissions: '0600'\n    content: " + encoded + "\nruncmd:\n  - [ sh, -lc, 'cd /home/runner/actions-runner && ./run.sh --jitconfig \"$(cat /run/runnerhub-jit)\"' ]\n"
	return os.WriteFile(path, []byte(content), 0600)
}
func jsonString(v string) string { b, _ := json.Marshal(v); return string(b) }
func safeName(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
func vmName(key string) string {
	name := "runnerhub-" + key
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.Trim(name, "-")
}
func (p *Provider) Inspect(ctx context.Context, h provider.WorkerHandle) (provider.WorkerState, error) {
	if vmID(h.ProviderID) == "" {
		return provider.WorkerState{}, errors.New("proxmox handle required")
	}
	var out struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := p.form(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/current", url.PathEscape(p.cfg.Node), vmID(h.ProviderID)), nil, &out); err != nil {
		if errors.Is(err, errNotFound) {
			return provider.WorkerState{State: core.WorkerDeleted}, nil
		}
		return provider.WorkerState{}, err
	}
	if out.Data.Status == "running" {
		return provider.WorkerState{State: core.WorkerReady, SafeDetail: "proxmox vm running"}, nil
	}
	return provider.WorkerState{State: core.WorkerCompleted, SafeDetail: "proxmox vm " + out.Data.Status}, nil
}
func (p *Provider) Stop(ctx context.Context, h provider.WorkerHandle) error {
	if vmID(h.ProviderID) == "" {
		return nil
	}
	err := p.task(ctx, http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/stop", url.PathEscape(p.cfg.Node), vmID(h.ProviderID)), nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}
func (p *Provider) Delete(ctx context.Context, h provider.WorkerHandle) error {
	if vmID(h.ProviderID) == "" {
		return nil
	}
	_ = p.Stop(ctx, h)
	err := p.task(ctx, http.MethodDelete, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s", url.PathEscape(p.cfg.Node), vmID(h.ProviderID)), nil)
	if errors.Is(err, errNotFound) {
		err = nil
	}
	if p.cfg.SnippetDir != "" {
		_ = os.Remove(filepath.Join(p.cfg.SnippetDir, "runnerhub-"+workerKey(h.ProviderID)+".yaml"))
	}
	return err
}
func (p *Provider) task(ctx context.Context, method, path string, form url.Values) error {
	var out struct {
		Data string `json:"data"`
	}
	if err := p.form(ctx, method, path, form, &out); err != nil {
		return err
	}
	if strings.TrimSpace(out.Data) == "" {
		return nil
	}
	return p.waitTask(ctx, out.Data)
}
func (p *Provider) waitTask(ctx context.Context, upid string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var out struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := p.form(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/tasks/%s/status", url.PathEscape(p.cfg.Node), url.PathEscape(upid)), nil, &out); err != nil {
			return err
		}
		if out.Data.Status == "stopped" {
			if out.Data.ExitStatus == "" || out.Data.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("proxmox task failed: %s", out.Data.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (p *Provider) form(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = bytes.NewBufferString(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.cfg.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+p.cfg.TokenID+"="+p.cfg.TokenSecret)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("proxmox returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func vmID(v string) string {
	if i := strings.IndexByte(v, '@'); i >= 0 {
		return v[:i]
	}
	return v
}
func workerKey(v string) string {
	if i := strings.IndexByte(v, '@'); i >= 0 {
		return safeName(v[i+1:])
	}
	return safeName(v)
}
func hashID(s string) int {
	h := 0
	for _, r := range s {
		h = (h*31 + int(r)) & 0x7fffffff
	}
	return h
}
