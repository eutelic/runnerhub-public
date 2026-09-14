package githubauth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
)

const (
	sessionCookieName = "runnerhub_session"
	apiVersion        = "2026-03-10"
)

type Config struct {
	ClientID     string
	ClientSecret string
	AppSlug      string
	PublicURL    string
	APIURL       string
	StateDir     string
	HTTPClient   *http.Client
}

type Manager struct {
	clientID     string
	clientSecret string
	appSlug      string
	publicURL    *url.URL
	apiURL       *url.URL
	stateDir     string
	http         *http.Client

	mu       sync.Mutex
	states   map[string]time.Time
	sessions map[string]session
}

type session struct {
	Login     string
	Token     string
	CSRF      string
	ExpiresAt time.Time
}

type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

type ConnectedRepository struct {
	ID             int64  `json:"id"`
	FullName       string `json:"fullName"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"defaultBranch"`
	InstallationID int64  `json:"installationId"`
	Account        string `json:"account"`
}

type Status struct {
	Enabled       bool   `json:"enabled"`
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user,omitempty"`
	CSRF          string `json:"csrf,omitempty"`
	InstallURL    string `json:"installUrl,omitempty"`
}

func New(c Config) (*Manager, error) {
	if strings.TrimSpace(c.ClientID) == "" {
		return nil, errors.New("github auth client id is required")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		return nil, errors.New("github auth client secret is required")
	}
	if strings.TrimSpace(c.AppSlug) == "" {
		return nil, errors.New("github app slug is required")
	}
	publicURL, err := url.Parse(strings.TrimRight(c.PublicURL, "/"))
	if err != nil || publicURL.Scheme == "" || publicURL.Host == "" {
		return nil, errors.New("runnerhub public URL is invalid")
	}
	apiRaw := strings.TrimSpace(c.APIURL)
	if apiRaw == "" {
		apiRaw = "https://api.github.com"
	}
	apiURL, err := url.Parse(strings.TrimRight(apiRaw, "/"))
	if err != nil || apiURL.Scheme == "" || apiURL.Host == "" {
		return nil, errors.New("github API URL is invalid")
	}
	stateDir := strings.TrimSpace(c.StateDir)
	if stateDir == "" {
		stateDir = "."
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Manager{
		clientID:     strings.TrimSpace(c.ClientID),
		clientSecret: strings.TrimSpace(c.ClientSecret),
		appSlug:      strings.TrimSpace(c.AppSlug),
		publicURL:    publicURL,
		apiURL:       apiURL,
		stateDir:     stateDir,
		http:         client,
		states:       map[string]time.Time{},
		sessions:     map[string]session{},
	}, nil
}

func (m *Manager) BeginLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not start github login", http.StatusInternalServerError)
		return
	}
	m.mu.Lock()
	m.states[state] = time.Now().Add(10 * time.Minute)
	m.mu.Unlock()

	q := url.Values{}
	q.Set("client_id", m.clientID)
	q.Set("redirect_uri", m.callbackURL())
	q.Set("state", state)
	q.Set("prompt", "select_account")
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func (m *Manager) Callback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" || !m.consumeState(state) {
		http.Error(w, "invalid or expired github login state", http.StatusBadRequest)
		return
	}
	token, expiresAt, err := m.exchangeCode(r, code)
	if err != nil {
		http.Error(w, "github login failed", http.StatusBadGateway)
		return
	}
	login, err := m.githubLogin(r, token)
	if err != nil {
		http.Error(w, "github identity lookup failed", http.StatusBadGateway)
		return
	}
	if err := m.claimOwner(login); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	sid, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	m.mu.Lock()
	m.sessions[sid] = session{Login: login, Token: token, CSRF: csrf, ExpiresAt: expiresAt}
	m.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.EqualFold(m.publicURL.Scheme, "https"),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (m *Manager) Install(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.sessionFor(r, false); !ok {
		http.Redirect(w, r, "/auth/github/login", http.StatusFound)
		return
	}
	target := "https://github.com/apps/" + url.PathEscape(m.appSlug) + "/installations/new"
	http.Redirect(w, r, target, http.StatusFound)
}

func (m *Manager) Setup(w http.ResponseWriter, r *http.Request) {
	s, ok := m.sessionFor(r, false)
	if !ok {
		http.Redirect(w, r, "/auth/github/login", http.StatusFound)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("installation_id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid github installation id", http.StatusBadRequest)
		return
	}
	installations, err := m.installations(r, s.Token)
	if err != nil {
		http.Error(w, "could not verify github installation", http.StatusBadGateway)
		return
	}
	found := false
	for _, installation := range installations {
		if installation.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "github installation is not accessible to the signed-in user", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/?github=installed", http.StatusFound)
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	s, ok := m.sessionFor(r, true)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	_ = s
	cookie, _ := r.Cookie(sessionCookieName)
	if cookie != nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.EqualFold(m.publicURL.Scheme, "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) Authenticate(r *http.Request) (auth.Principal, bool) {
	s, ok := m.sessionFor(r, r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions)
	if !ok {
		return auth.Principal{}, false
	}
	return auth.Principal{Name: "github:" + s.Login, Role: auth.RoleAdmin}, true
}

func (m *Manager) Status(r *http.Request) Status {
	out := Status{Enabled: true, InstallURL: "/auth/github/install"}
	if s, ok := m.sessionFor(r, false); ok {
		out.Authenticated = true
		out.User = s.Login
		out.CSRF = s.CSRF
	}
	return out
}

func (m *Manager) ConnectedRepositories(r *http.Request) ([]ConnectedRepository, error) {
	s, ok := m.sessionFor(r, false)
	if !ok {
		return nil, errors.New("github login required")
	}
	installations, err := m.installations(r, s.Token)
	if err != nil {
		return nil, err
	}
	var out []ConnectedRepository
	for _, installation := range installations {
		repos, err := m.repositoriesForInstallation(r, s.Token, installation)
		if err != nil {
			return nil, err
		}
		out = append(out, repos...)
	}
	return out, nil
}

func (m *Manager) ValidateRepository(r *http.Request, installationID int64, fullName string) (ConnectedRepository, error) {
	repos, err := m.ConnectedRepositories(r)
	if err != nil {
		return ConnectedRepository{}, err
	}
	for _, repo := range repos {
		if repo.InstallationID == installationID && strings.EqualFold(repo.FullName, strings.TrimSpace(fullName)) {
			return repo, nil
		}
	}
	return ConnectedRepository{}, errors.New("repository is not accessible through the selected github app installation")
}

func (m *Manager) callbackURL() string {
	u := *m.publicURL
	u.Path = strings.TrimRight(u.Path, "/") + "/auth/github/callback"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (m *Manager) sessionFor(r *http.Request, requireCSRF bool) (session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return session{}, false
	}
	m.mu.Lock()
	s, ok := m.sessions[cookie.Value]
	if ok && time.Now().After(s.ExpiresAt) {
		delete(m.sessions, cookie.Value)
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return session{}, false
	}
	if requireCSRF && r.Header.Get("X-RunnerHub-CSRF") != s.CSRF {
		return session{}, false
	}
	return s, true
}

func (m *Manager) consumeState(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	expiresAt, ok := m.states[state]
	delete(m.states, state)
	return ok && time.Now().Before(expiresAt)
}

func (m *Manager) exchangeCode(r *http.Request, code string) (string, time.Time, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     m.clientID,
		"client_secret": m.clientSecret,
		"code":          code,
		"redirect_uri":  m.callbackURL(),
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", time.Time{}, fmt.Errorf("github oauth exchange returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, err
	}
	if out.Error != "" {
		return "", time.Time{}, fmt.Errorf("github oauth: %s: %s", out.Error, out.Description)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, errors.New("github oauth returned an empty access token")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = int64((8 * time.Hour).Seconds())
	}
	return out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

func (m *Manager) githubLogin(r *http.Request, token string) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := m.apiGET(r, token, "/user", &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Login) == "" {
		return "", errors.New("github returned an empty login")
	}
	return strings.TrimSpace(out.Login), nil
}

func (m *Manager) installations(r *http.Request, token string) ([]Installation, error) {
	var out struct {
		Installations []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installations"`
	}
	if err := m.apiGET(r, token, "/user/installations?per_page=100", &out); err != nil {
		return nil, err
	}
	installations := make([]Installation, 0, len(out.Installations))
	for _, item := range out.Installations {
		if item.ID > 0 {
			installations = append(installations, Installation{ID: item.ID, Account: item.Account.Login})
		}
	}
	return installations, nil
}

func (m *Manager) repositoriesForInstallation(r *http.Request, token string, installation Installation) ([]ConnectedRepository, error) {
	var all []ConnectedRepository
	for page := 1; ; page++ {
		var out struct {
			Repositories []struct {
				ID            int64  `json:"id"`
				FullName      string `json:"full_name"`
				Private       bool   `json:"private"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repositories"`
		}
		path := fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", installation.ID, page)
		if err := m.apiGET(r, token, path, &out); err != nil {
			return nil, err
		}
		for _, repo := range out.Repositories {
			all = append(all, ConnectedRepository{
				ID:             repo.ID,
				FullName:       repo.FullName,
				Private:        repo.Private,
				DefaultBranch:  repo.DefaultBranch,
				InstallationID: installation.ID,
				Account:        installation.Account,
			})
		}
		if len(out.Repositories) < 100 {
			break
		}
	}
	return all, nil
}

func (m *Manager) apiGET(r *http.Request, token, path string, out any) error {
	u := *m.apiURL
	u.Path = strings.TrimRight(u.Path, "/") + strings.SplitN(path, "?", 2)[0]
	if parts := strings.SplitN(path, "?", 2); len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *Manager) claimOwner(login string) error {
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		return fmt.Errorf("prepare github auth state: %w", err)
	}
	path := filepath.Join(m.stateDir, "github-owner")
	current, err := os.ReadFile(path)
	if err == nil {
		if strings.EqualFold(strings.TrimSpace(string(current)), login) {
			return nil
		}
		return fmt.Errorf("runnerhub is owned by github user %s", strings.TrimSpace(string(current)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read github owner state: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return m.claimOwner(login)
		}
		return fmt.Errorf("claim github owner: %w", err)
	}
	if _, err := f.WriteString(login + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write github owner state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close github owner state: %w", err)
	}
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
