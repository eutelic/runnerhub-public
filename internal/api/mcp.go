package api

import (
	"encoding/json"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/eutelic/runnerhub/internal/auth"
)

func (s *Service) mcpPairStatus(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		http.Error(w, "pairing unavailable", http.StatusServiceUnavailable)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		token = ""
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": s.MCP.Status(token)})
}

func (s *Service) mcpPairRequest(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		http.Error(w, "pairing unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 513))
	if err != nil || len(data) > 512 {
		http.Error(w, "invalid request size", http.StatusBadRequest)
		return
	}
	var input struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		source = r.RemoteAddr
	}
	status, err := s.MCP.Request(input.Token, input.Name, source)
	if err != nil {
		http.Error(w, "connection request rejected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (s *Service) mcpConnections(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil || s.Auth == nil {
		http.Error(w, "pairing unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, err := s.Auth.Authenticate(r)
	if err != nil {
		http.Error(w, "sign in as RunnerHub administrator", http.StatusUnauthorized)
		return
	}
	if principal.MCP || principal.Role != auth.RoleAdmin {
		http.Error(w, "administrator required", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		if !mcpSameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			http.Error(w, "invalid form origin", http.StatusForbidden)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 1025))
		if err != nil || len(data) > 1024 {
			http.Error(w, "invalid form size", http.StatusBadRequest)
			return
		}
		v, err := url.ParseQuery(string(data))
		if err != nil || len(v["id"]) != 1 || len(v["action"]) != 1 ||
			len(v.Get("id")) > 128 {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}
		if err := s.MCP.Decide(v.Get("id"), v.Get("action")); err != nil {
			http.Error(w, "unknown or expired connection", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/mcp/connections", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := mcpConnectionsPage.Execute(w, s.MCP.List()); err != nil {
		return
	}
}

func mcpSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "same-origin" {
		return false
	}
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}

var mcpConnectionsPage = template.Must(template.New("connections").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width">
<title>RunnerHub MCP connections</title>
<style>
:root {color-scheme:dark;font:15px/1.5 system-ui,sans-serif;background:#101923;color:#e6edf5}
body{max-width:950px;margin:5vh auto;padding:20px}
main{background:#172434;border:1px solid #34475b;border-radius:12px;padding:24px}
a{color:#9fd3ed} table{width:100%;border-collapse:collapse}
td,th{text-align:left;padding:12px;border-bottom:1px solid #34475b;vertical-align:top}
button{background:#263c53;color:inherit;border:1px solid #49617a;border-radius:7px;padding:7px 11px;cursor:pointer}
button:hover{background:#355675}form{display:inline-block;margin:3px}
.warning{color:#ffcb9a}.small{font-size:13px;color:#bcc8d8}
</style></head><body><main>
<p><a href="/">Back to RunnerHub</a></p><h1>MCP connections</h1>
<p>Approve a local MCP client to inspect RunnerHub. Viewer access is read-only.
Operator access additionally allows requesting <strong>one worker</strong> in an existing pool.
Other administrative API operations remain blocked. Approvals persist until revoked.</p>
<p class="warning">Only approve clients you recognize. On an unencrypted LAN, client tokens can be intercepted; prefer HTTPS.</p>
<table><thead><tr><th>Client</th><th>Requested</th><th>Access</th><th>Action</th></tr></thead>
<tbody>{{range .}}<tr><td>{{.Name}}</td><td>{{.Created.Format "2006-01-02 15:04 MST"}}</td>
<td>{{if .Approved}}{{.Scope}}{{else}}Pending (10 min expiry){{end}}</td><td>
{{if .Approved}}
<form action="/mcp/connections" method="post"><input type="hidden" name="id" value="{{.RequestID}}">
<button type="submit" name="action" value="revoke">Revoke</button></form>
{{else}}
<form action="/mcp/connections" method="post"><input type="hidden" name="id" value="{{.RequestID}}">
<button type="submit" name="action" value="approve-viewer">Approve read-only</button>
<button type="submit" name="action" value="approve-operator">Approve operator</button>
<button type="submit" name="action" value="deny">Deny</button></form>
{{end}}
</td></tr>{{else}}<tr><td colspan="4" class="small">No MCP connections</td></tr>{{end}}</tbody>
</table></main></body></html>`))
