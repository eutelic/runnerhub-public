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
	sessionCookieName          = "runnerhub_session"
	sessionsFileName           = "github-sessions.json"
	apiVersion                 = "2026-03-10"
	persistentSessionLifetime  = 90 * 24 * time.Hour
	tokenRefreshSkew           = 5 * time.Minute
	discoveryCacheTTL          = 60 * time.Second
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

	discoveryMu          sync.Mutex
	installationsCache   map[string]installationCacheEntry
	repositoriesCache    map[string]repositoryCacheEntry
	installationsPending map[string]*installationFlight
	repositoriesPending  map[string]*repositoryFlight
}

type session struct {
	Login            string    `json:"login"`
	Token            string    `json:"token"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	CSRF             string    `json:"csrf"`
	ExpiresAt        time.Time `json:"expiresAt"`
	TokenExpiresAt   time.Time `json:"tokenExpiresAt,omitempty"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitempty"`
}

type persistedSessions struct {
	Version  int                `json:"version"`
	Sessions map[string]session `json:"sessions"`
}

type oauthTokens struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

type oauthTokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	Description           string `json:"error_description"`
}

type Installation struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	AccountType string `json:"accountType,omitempty"`
}

type ConnectedRepository struct {
	ID             int64  `json:"id"`
	FullName       string `json:"fullName"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"defaultBranch"`
	InstallationID int64  `json:"installationId"`
	Account        string `json:"account"`
	AccountType    string `json:"accountType,omitempty"`
}

type installationCacheEntry struct {
	Items     []Installation
	ExpiresAt time.Time
}

type repositoryCacheEntry struct {
	Items     []ConnectedRepository
	ExpiresAt time.Time
}

type installationFlight struct {
	done  chan struct{}
	items []Installation
	err   error
}

type repositoryFlight struct {
	done  chan struct{}
	items []ConnectedRepository
	err   error
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
	m := &Manager{
		clientID:     strings.TrimSpace(c.ClientID),
		clientSecret: strings.TrimSpace(c.ClientSecret),
		appSlug:      strings.TrimSpace(c.AppSlug),
		publicURL:    publicURL,
		apiURL:       apiURL,
		stateDir:     stateDir,
		http:         client,
		states:               map[string]time.Time{},
		sessions:             map[string]session{},
		installationsCache:   map[string]installationCacheEntry{},
		repositoriesCache:    map[string]repositoryCacheEntry{},
		installationsPending: map[string]*installationFlight{},
		repositoriesPending:  map[string]*repositoryFlight{},
	}
	if err := m.loadSessions(); err != nil {
		return nil, err
	}
	return m, nil
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
	tokens, err := m.exchangeCode(r, code)
	if err != nil {
		http.Error(w, "github login failed", http.StatusBadGateway)
		return
	}
	login, err := m.githubLogin(r, tokens.AccessToken)
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
	expiresAt := time.Now().Add(persistentSessionLifetime)
	if !tokens.RefreshExpiresAt.IsZero() && tokens.RefreshExpiresAt.Before(expiresAt) {
		expiresAt = tokens.RefreshExpiresAt
	} else if tokens.RefreshToken == "" && !tokens.AccessExpiresAt.IsZero() && tokens.AccessExpiresAt.Before(expiresAt) {
		expiresAt = tokens.AccessExpiresAt
	}
	s := session{
		Login: login, Token: tokens.AccessToken, RefreshToken: tokens.RefreshToken, CSRF: csrf,
		ExpiresAt: expiresAt, TokenExpiresAt: tokens.AccessExpiresAt, RefreshExpiresAt: tokens.RefreshExpiresAt,
	}
	m.mu.Lock()
	m.sessions[sid] = s
	persistErr := m.persistSessionsLocked()
	if persistErr != nil {
		delete(m.sessions, sid)
	}
	m.mu.Unlock()
	if persistErr != nil {
		http.Error(w, "could not persist github session", http.StatusInternalServerError)
		return
	}

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
	m.invalidateDiscovery(s.Login)
	installations, err := m.cachedInstallations(r, s)
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
	var persistErr error
	if cookie != nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
		persistErr = m.persistSessionsLocked()
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
	if persistErr != nil {
		http.Error(w, "could not persist github logout", http.StatusInternalServerError)
		return
	}
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

func cloneInstallations(items []Installation) []Installation {
	return append([]Installation(nil), items...)
}

func cloneRepositories(items []ConnectedRepository) []ConnectedRepository {
	return append([]ConnectedRepository(nil), items...)
}

func discoveryKey(s session) string {
	return strings.ToLower(strings.TrimSpace(s.Login))
}

func (m *Manager) invalidateDiscovery(login string) {
	key := strings.ToLower(strings.TrimSpace(login))
	m.discoveryMu.Lock()
	delete(m.installationsCache, key)
	delete(m.repositoriesCache, key)
	m.discoveryMu.Unlock()
}

func (m *Manager) cachedInstallations(r *http.Request, s session) ([]Installation, error) {
	key := discoveryKey(s)
	now := time.Now()
	m.discoveryMu.Lock()
	if entry, ok := m.installationsCache[key]; ok && entry.ExpiresAt.After(now) {
		items := cloneInstallations(entry.Items)
		m.discoveryMu.Unlock()
		return items, nil
	}
	if pending, ok := m.installationsPending[key]; ok {
		done := pending.done
		m.discoveryMu.Unlock()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-done:
		}
		return cloneInstallations(pending.items), pending.err
	}
	pending := &installationFlight{done: make(chan struct{})}
	m.installationsPending[key] = pending
	m.discoveryMu.Unlock()

	items, err := m.installations(r, s.Token)
	m.discoveryMu.Lock()
	pending.items = cloneInstallations(items)
	pending.err = err
	if err == nil {
		m.installationsCache[key] = installationCacheEntry{Items: cloneInstallations(items), ExpiresAt: time.Now().Add(discoveryCacheTTL)}
	}
	delete(m.installationsPending, key)
	close(pending.done)
	m.discoveryMu.Unlock()
	return cloneInstallations(items), err
}

func (m *Manager) cachedRepositories(r *http.Request, s session) ([]ConnectedRepository, error) {
	key := discoveryKey(s)
	now := time.Now()
	m.discoveryMu.Lock()
	if entry, ok := m.repositoriesCache[key]; ok && entry.ExpiresAt.After(now) {
		items := cloneRepositories(entry.Items)
		m.discoveryMu.Unlock()
		return items, nil
	}
	if pending, ok := m.repositoriesPending[key]; ok {
		done := pending.done
		m.discoveryMu.Unlock()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-done:
		}
		return cloneRepositories(pending.items), pending.err
	}
	pending := &repositoryFlight{done: make(chan struct{})}
	m.repositoriesPending[key] = pending
	m.discoveryMu.Unlock()

	installations, err := m.cachedInstallations(r, s)
	var items []ConnectedRepository
	if err == nil {
		for _, installation := range installations {
			var repos []ConnectedRepository
			repos, err = m.repositoriesForInstallation(r, s.Token, installation)
			if err != nil {
				break
			}
			items = append(items, repos...)
		}
	}

	m.discoveryMu.Lock()
	pending.items = cloneRepositories(items)
	pending.err = err
	if err == nil {
		m.repositoriesCache[key] = repositoryCacheEntry{Items: cloneRepositories(items), ExpiresAt: time.Now().Add(discoveryCacheTTL)}
	}
	delete(m.repositoriesPending, key)
	close(pending.done)
	m.discoveryMu.Unlock()
	return cloneRepositories(items), err
}

func (m *Manager) AccessibleInstallations(r *http.Request) ([]Installation, error) {
	s, ok := m.sessionFor(r, false)
	if !ok {
		return nil, errors.New("github login required")
	}
	return m.cachedInstallations(r, s)
}

func (m *Manager) ConnectedRepositories(r *http.Request) ([]ConnectedRepository, error) {
	s, ok := m.sessionFor(r, false)
	if !ok {
		return nil, errors.New("github login required")
	}
	return m.cachedRepositories(r, s)
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
	now := time.Now()
	m.mu.Lock()
	s, ok := m.sessions[cookie.Value]
	if ok && now.After(s.ExpiresAt) {
		delete(m.sessions, cookie.Value)
		_ = m.persistSessionsLocked()
		ok = false
	}
	if ok && !s.TokenExpiresAt.IsZero() && now.Add(tokenRefreshSkew).After(s.TokenExpiresAt) {
		canRefresh := s.RefreshToken != "" && (s.RefreshExpiresAt.IsZero() || now.Before(s.RefreshExpiresAt))
		if canRefresh {
			if tokens, refreshErr := m.refreshAccessToken(r, s.RefreshToken); refreshErr == nil {
			s.Token = tokens.AccessToken
			if tokens.RefreshToken != "" {
				s.RefreshToken = tokens.RefreshToken
			}
			s.TokenExpiresAt = tokens.AccessExpiresAt
			if !tokens.RefreshExpiresAt.IsZero() {
				s.RefreshExpiresAt = tokens.RefreshExpiresAt
			}
			m.sessions[cookie.Value] = s
			_ = m.persistSessionsLocked()
			} else if now.After(s.TokenExpiresAt) {
				delete(m.sessions, cookie.Value)
				_ = m.persistSessionsLocked()
				ok = false
			}
		} else if now.After(s.TokenExpiresAt) {
			delete(m.sessions, cookie.Value)
			_ = m.persistSessionsLocked()
			ok = false
		}
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

func (m *Manager) exchangeCode(r *http.Request, code string) (oauthTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     m.clientID,
		"client_secret": m.clientSecret,
		"code":          code,
		"redirect_uri":  m.callbackURL(),
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(body))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return m.doOAuthTokenRequest(req, "exchange")
}

func (m *Manager) refreshAccessToken(r *http.Request, refreshToken string) (oauthTokens, error) {
	form := url.Values{}
	form.Set("client_id", m.clientID)
	form.Set("client_secret", m.clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return m.doOAuthTokenRequest(req, "refresh")
}

func (m *Manager) doOAuthTokenRequest(req *http.Request, operation string) (oauthTokens, error) {
	resp, err := m.http.Do(req)
	if err != nil {
		return oauthTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return oauthTokens{}, fmt.Errorf("github oauth %s returned %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return oauthTokens{}, err
	}
	if out.Error != "" {
		return oauthTokens{}, fmt.Errorf("github oauth: %s: %s", out.Error, out.Description)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return oauthTokens{}, errors.New("github oauth returned an empty access token")
	}
	now := time.Now()
	tokens := oauthTokens{AccessToken: strings.TrimSpace(out.AccessToken), RefreshToken: strings.TrimSpace(out.RefreshToken)}
	if out.ExpiresIn > 0 {
		tokens.AccessExpiresAt = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshTokenExpiresIn > 0 {
		tokens.RefreshExpiresAt = now.Add(time.Duration(out.RefreshTokenExpiresIn) * time.Second)
	}
	return tokens, nil
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
				Type  string `json:"type"`
			} `json:"account"`
		} `json:"installations"`
	}
	if err := m.apiGET(r, token, "/user/installations?per_page=100", &out); err != nil {
		return nil, err
	}
	installations := make([]Installation, 0, len(out.Installations))
	for _, item := range out.Installations {
		if item.ID > 0 {
			installations = append(installations, Installation{ID: item.ID, Account: item.Account.Login, AccountType: item.Account.Type})
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
				AccountType:    installation.AccountType,
			})
		}
		if len(out.Repositories) < 100 {
			break
		}
	}
	return all, nil
}

// githubAPIError preserves safe API failure metadata for queue diagnostics.
// Never persist Authorization or raw response bodies in this error.
type githubAPIError struct {
	StatusCode int
	Message string
	RequestID string
	RateRemaining string
	RateReset string
	RetryAfter string
}

func (e *githubAPIError) Error() string {
	return fmt.Sprintf("github api returned %d: %s", e.StatusCode, e.Message)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var payload struct { Message string `json:"message"` }
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		// GitHub's error message is untrusted: bound its length, never return the
		// raw body (which may contain documentation URLs or other arbitrary data).
		if len(message) > 240 { message = message[:240] }
		requestID := strings.TrimSpace(resp.Header.Get("X-GitHub-Request-Id"))
		if len(requestID) > 80 { requestID = "" }
		return &githubAPIError{
			StatusCode: resp.StatusCode, Message: message, RequestID: requestID,
			RateRemaining: resp.Header.Get("X-RateLimit-Remaining"),
			RateReset: resp.Header.Get("X-RateLimit-Reset"),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *Manager) loadSessions() error {
	path := filepath.Join(m.stateDir, sessionsFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read github sessions: %w", err)
	}
	var persisted persistedSessions
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("decode github sessions: %w", err)
	}
	now := time.Now()
	for sid, s := range persisted.Sessions {
		if strings.TrimSpace(sid) == "" || strings.TrimSpace(s.Login) == "" || strings.TrimSpace(s.Token) == "" || strings.TrimSpace(s.CSRF) == "" || !now.Before(s.ExpiresAt) {
			continue
		}
		m.sessions[sid] = s
	}
	return nil
}

func (m *Manager) persistSessionsLocked() error {
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		return fmt.Errorf("prepare github auth state: %w", err)
	}
	payload, err := json.Marshal(persistedSessions{Version: 1, Sessions: m.sessions})
	if err != nil {
		return fmt.Errorf("encode github sessions: %w", err)
	}
	tmp, err := os.CreateTemp(m.stateDir, ".github-sessions-*")
	if err != nil {
		return fmt.Errorf("create github sessions temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure github sessions temp file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write github sessions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync github sessions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close github sessions: %w", err)
	}
	path := filepath.Join(m.stateDir, sessionsFileName)
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace github sessions: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure github sessions file: %w", err)
	}
	return nil
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
