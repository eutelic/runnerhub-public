package githubrunner

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

type GitHubAppConfig struct {
	Issuer        string
	AppSlug       string
	PrivateKeyPEM []byte
	BaseURL       string
	HTTPClient    *http.Client
}

type cachedInstallationToken struct {
	token     string
	expiresAt time.Time
}

type GitHubAppTokenSource struct {
	issuer     string
	appSlug    string
	privateKey *rsa.PrivateKey
	baseURL    *url.URL
	client     *http.Client
	now        func() time.Time

	mu              sync.Mutex
	resolvedAppID   int64
	installationIDs map[string]int64
	tokens          map[string]cachedInstallationToken
}

func NewGitHubAppTokenSource(config GitHubAppConfig) (*GitHubAppTokenSource, error) {
	if strings.TrimSpace(config.Issuer) == "" {
		return nil, errors.New("github app issuer is required")
	}
	privateKey, err := parseRSAPrivateKey(config.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	baseURL, err := parseAPIBase(config.BaseURL)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &GitHubAppTokenSource{
		issuer:          strings.TrimSpace(config.Issuer),
		appSlug:         strings.TrimSpace(config.AppSlug),
		privateKey:      privateKey,
		baseURL:         baseURL,
		client:          client,
		now:             time.Now,
		installationIDs: map[string]int64{},
		tokens:          map[string]cachedInstallationToken{},
	}, nil
}

func (s *GitHubAppTokenSource) Token(ctx context.Context, scope core.GitHubScope) (string, error) {
	if err := core.ValidateGitHubScope(scope); err != nil {
		return "", err
	}
	key := scopeCacheKey(scope)
	now := s.now()

	s.mu.Lock()
	cached, ok := s.tokens[key]
	s.mu.Unlock()
	if ok && cached.token != "" && cached.expiresAt.After(now.Add(2*time.Minute)) {
		return cached.token, nil
	}

	installationID, err := s.installationID(ctx, scope)
	if err != nil {
		return "", err
	}
	token, expiresAt, err := s.createInstallationToken(ctx, scope, installationID)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.tokens[key] = cachedInstallationToken{token: token, expiresAt: expiresAt}
	s.mu.Unlock()
	return token, nil
}

func (s *GitHubAppTokenSource) installationID(ctx context.Context, scope core.GitHubScope) (int64, error) {
	key := scopeCacheKey(scope)
	s.mu.Lock()
	id := s.installationIDs[key]
	s.mu.Unlock()
	if id > 0 {
		return id, nil
	}

	jwt, err := s.appJWT(ctx)
	if err != nil {
		return 0, err
	}
	var response struct {
		ID int64 `json:"id"`
	}
	if err := s.appRequest(ctx, http.MethodGet, installationPath(scope), jwt, nil, http.StatusOK, &response); err != nil {
		return 0, fmt.Errorf("resolve github app installation: %w", err)
	}
	if response.ID <= 0 {
		return 0, errors.New("github returned an invalid installation id")
	}

	s.mu.Lock()
	s.installationIDs[key] = response.ID
	s.mu.Unlock()
	return response.ID, nil
}

func (s *GitHubAppTokenSource) createInstallationToken(ctx context.Context, scope core.GitHubScope, installationID int64) (string, time.Time, error) {
	jwt, err := s.appJWT(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	body := map[string]any{}
	if scope.Kind == core.ScopeRepository {
		body["repositories"] = []string{scope.Repository}
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := s.appRequest(ctx, http.MethodPost, path, jwt, body, http.StatusCreated, &response); err != nil {
		return "", time.Time{}, fmt.Errorf("create github app installation token: %w", err)
	}
	if strings.TrimSpace(response.Token) == "" || response.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("github returned an incomplete installation token")
	}
	return response.Token, response.ExpiresAt, nil
}

func (s *GitHubAppTokenSource) appJWT(ctx context.Context) (string, error) {
	issuer, err := s.jwtIssuer(ctx)
	if err != nil {
		return "", err
	}
	now := s.now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": issuer,
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign github app jwt: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *GitHubAppTokenSource) jwtIssuer(ctx context.Context) (any, error) {
	if id, err := strconv.ParseInt(strings.TrimSpace(s.issuer), 10, 64); err == nil && id > 0 {
		return id, nil
	}
	if s.appSlug == "" {
		// GitHub documents Client ID strings as valid JWT issuers. Preserve that
		// behavior when no slug is available for numeric-ID compatibility.
		return s.issuer, nil
	}

	s.mu.Lock()
	id := s.resolvedAppID
	s.mu.Unlock()
	if id > 0 {
		return id, nil
	}

	resolved, err := s.resolveAppID(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.resolvedAppID = resolved
	s.mu.Unlock()
	return resolved, nil
}

func (s *GitHubAppTokenSource) resolveAppID(ctx context.Context) (int64, error) {
	endpoint, err := apiEndpoint(s.baseURL, "/apps/"+url.PathEscape(s.appSlug))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", defaultAPIVersion)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return 0, fmt.Errorf("resolve github app %q returned %d: %s", s.appSlug, resp.StatusCode, sanitizeErrorBody(limited, ""))
	}

	var app struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
		Slug     string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&app); err != nil {
		return 0, err
	}
	if app.ID <= 0 {
		return 0, errors.New("github returned an invalid app id")
	}
	if app.ClientID == "" || !strings.EqualFold(app.ClientID, s.issuer) {
		return 0, fmt.Errorf("github app slug %q resolves to client id %q, not configured client id %q", s.appSlug, app.ClientID, s.issuer)
	}
	if app.Slug != "" && !strings.EqualFold(app.Slug, s.appSlug) {
		return 0, fmt.Errorf("github returned unexpected app slug %q", app.Slug)
	}
	return app.ID, nil
}

func (s *GitHubAppTokenSource) appRequest(ctx context.Context, method, path, jwt string, body any, expectedStatus int, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint, err := apiEndpoint(s.baseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-GitHub-Api-Version", defaultAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("github api %s %s returned %d: %s", method, path, resp.StatusCode, sanitizeErrorBody(limited, jwt))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func installationPath(scope core.GitHubScope) string {
	owner := url.PathEscape(scope.Owner)
	if scope.Kind == core.ScopeOrganization {
		return "/orgs/" + owner + "/installation"
	}
	return "/repos/" + owner + "/" + url.PathEscape(scope.Repository) + "/installation"
}

func scopeCacheKey(scope core.GitHubScope) string {
	if scope.Kind == core.ScopeOrganization {
		return "org:" + strings.ToLower(scope.Owner)
	}
	return "repo:" + strings.ToLower(scope.Owner) + "/" + strings.ToLower(scope.Repository)
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("github app private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github app private key is not a supported RSA key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key must be RSA")
	}
	return key, nil
}
