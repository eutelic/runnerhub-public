package githubrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/example-user/runnerhub/internal/core"
)

const defaultAPIVersion = "2026-03-10"

type TokenSource interface {
	Token(ctx context.Context, scope core.GitHubScope) (string, error)
}

type HTTPBackend struct {
	baseURL    *url.URL
	httpClient *http.Client
	tokens     TokenSource
	apiVersion string
}

func NewHTTPBackend(client *http.Client, tokens TokenSource) (*HTTPBackend, error) {
	return NewHTTPBackendWithBaseURL(client, tokens, "https://api.github.com")
}

func NewHTTPBackendWithBaseURL(client *http.Client, tokens TokenSource, rawBaseURL string) (*HTTPBackend, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if tokens == nil {
		return nil, errors.New("token source is required")
	}
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse github api base url: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("github api base url must use http or https")
	}
	return &HTTPBackend{
		baseURL:    baseURL,
		httpClient: client,
		tokens:     tokens,
		apiVersion: defaultAPIVersion,
	}, nil
}

func (b *HTTPBackend) GenerateJITConfig(ctx context.Context, req JITConfigRequest) (JITConfig, error) {
	if err := ValidateJITConfigRequest(req); err != nil {
		return JITConfig{}, err
	}
	workFolder := req.WorkFolder
	if workFolder == "" {
		workFolder = "_work"
	}
	payload := struct {
		Name          string   `json:"name"`
		RunnerGroupID int64    `json:"runner_group_id"`
		Labels        []string `json:"labels"`
		WorkFolder    string   `json:"work_folder"`
	}{
		Name:          req.RunnerName,
		RunnerGroupID: req.RunnerGroupID,
		Labels:        req.Labels,
		WorkFolder:    workFolder,
	}

	var response struct {
		Runner struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"runner"`
		EncodedJITConfig string `json:"encoded_jit_config"`
	}
	if err := b.doJSON(ctx, req.Scope, http.MethodPost, b.jitPath(req.Scope), payload, http.StatusCreated, &response); err != nil {
		return JITConfig{}, err
	}
	if response.Runner.ID <= 0 || response.Runner.Name == "" || response.EncodedJITConfig == "" {
		return JITConfig{}, errors.New("github returned incomplete jit configuration")
	}
	return JITConfig{
		RunnerID:      response.Runner.ID,
		RunnerName:    response.Runner.Name,
		EncodedConfig: response.EncodedJITConfig,
	}, nil
}

func (b *HTTPBackend) GetRunner(ctx context.Context, scope core.GitHubScope, runnerID int64) (Runner, error) {
	if err := core.ValidateGitHubScope(scope); err != nil {
		return Runner{}, err
	}
	if runnerID <= 0 {
		return Runner{}, errors.New("runner id must be positive")
	}
	var response struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Busy   bool   `json:"busy"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := b.doJSON(ctx, scope, http.MethodGet, b.runnerPath(scope, runnerID), nil, http.StatusOK, &response); err != nil {
		return Runner{}, err
	}
	labels := make([]string, 0, len(response.Labels))
	for _, label := range response.Labels {
		labels = append(labels, label.Name)
	}
	return Runner{ID: response.ID, Name: response.Name, Status: response.Status, Busy: response.Busy, Labels: labels}, nil
}

func (b *HTTPBackend) DeleteRunner(ctx context.Context, scope core.GitHubScope, runnerID int64) error {
	if err := core.ValidateGitHubScope(scope); err != nil {
		return err
	}
	if runnerID <= 0 {
		return errors.New("runner id must be positive")
	}
	return b.doJSON(ctx, scope, http.MethodDelete, b.runnerPath(scope, runnerID), nil, http.StatusNoContent, nil)
}

func (b *HTTPBackend) doJSON(ctx context.Context, scope core.GitHubScope, method, path string, body any, expectedStatus int, out any) error {
	token, err := b.tokens.Token(ctx, scope)
	if err != nil {
		return fmt.Errorf("get github token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("github token source returned an empty token")
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode github request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	relative, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("parse github request path: %w", err)
	}
	endpoint := b.baseURL.ResolveReference(relative)
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return fmt.Errorf("create github request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", b.apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != expectedStatus {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("github api %s %s returned %d: %s", method, path, resp.StatusCode, sanitizeErrorBody(limited))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func (b *HTTPBackend) jitPath(scope core.GitHubScope) string {
	return b.scopeBasePath(scope) + "/actions/runners/generate-jitconfig"
}

func (b *HTTPBackend) runnerPath(scope core.GitHubScope, runnerID int64) string {
	return b.scopeBasePath(scope) + "/actions/runners/" + strconv.FormatInt(runnerID, 10)
}

func (b *HTTPBackend) scopeBasePath(scope core.GitHubScope) string {
	owner := url.PathEscape(scope.Owner)
	if scope.Kind == core.ScopeOrganization {
		return "/orgs/" + owner
	}
	return "/repos/" + owner + "/" + url.PathEscape(scope.Repository)
}

func sanitizeErrorBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return http.StatusText(http.StatusInternalServerError)
	}
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
