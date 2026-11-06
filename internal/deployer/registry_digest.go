package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ImageDigestResolver reports the immutable OCI digest of an exact GHCR tag.
// The default resolver uses anonymous GHCR pull authentication. Private
// packages without anonymous access fail closed rather than guessing.
type ImageDigestResolver interface {
	Digest(context.Context, string) (string, error)
}

type GHCRDigestResolver struct {
	Client  *http.Client
	// BaseURL is injected only in tests; production uses https://ghcr.io.
	BaseURL string
}

var ghcrTagPattern = regexp.MustCompile(`^ghcr\.io/([a-z0-9_.-]+)/([a-z0-9_.-]+):(latest|sha-[a-f0-9]{40})$`)
var sha256ImageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var bearerParameterPattern = regexp.MustCompile(`([a-z_]+)="([^"]*)"`)

func ValidImageDigest(digest string) bool {
	return sha256ImageDigestPattern.MatchString(digest)
}

func (r GHCRDigestResolver) Digest(ctx context.Context, image string) (string, error) {
	ref := strings.ToLower(strings.TrimSpace(image))
	match := ghcrTagPattern.FindStringSubmatch(ref)
	if len(match) != 4 || strings.Contains(match[1], "..") || strings.Contains(match[2], "..") {
		return "", errors.New("registry digest lookup only supports exact ghcr.io/owner/repository:latest or :sha-<40-character commit> images")
	}
	base := strings.TrimSpace(r.BaseURL)
	if base == "" {
		base = "https://ghcr.io"
	}
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" || (origin.Scheme != "https" && !(r.BaseURL != "" && origin.Scheme == "http")) || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return "", errors.New("invalid GHCR registry endpoint")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	manifest := origin.String() + "/v2/" + match[1] + "/" + match[2] + "/manifests/" + match[3]
	scope := "repository:" + match[1] + "/" + match[2] + ":pull"
	digest, challenge, err := ghcrManifestDigest(ctx, client, manifest, "")
	if err == nil {
		return digest, nil
	}
	if challenge == "" {
		return "", err
	}
	params := make(map[string]string)
	for _, part := range bearerParameterPattern.FindAllStringSubmatch(challenge, -1) {
		params[part[1]] = part[2]
	}
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") ||
		params["service"] != "ghcr.io" || params["scope"] != scope {
		return "", errors.New("unexpected GHCR authentication challenge")
	}
	tokenURL, parseErr := url.Parse(params["realm"])
	if parseErr != nil || tokenURL.Host != origin.Host || tokenURL.Scheme != origin.Scheme ||
		tokenURL.Path != "/token" || tokenURL.User != nil || tokenURL.Fragment != "" {
		return "", errors.New("GHCR authentication challenge points outside the registry token service")
	}
	query := tokenURL.Query()
	query.Set("scope", scope)
	query.Set("service", "ghcr.io")
	tokenURL.RawQuery = query.Encode()
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if reqErr != nil {
		return "", reqErr
	}
	response, getErr := client.Do(req)
	if getErr != nil {
		return "", fmt.Errorf("obtain GHCR anonymous pull token: %w", getErr)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GHCR package cannot be verified anonymously (token HTTP %d); keep automatic deployment disabled", response.StatusCode)
	}
	var payload struct {
		Token string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode GHCR pull token: %w", err)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("GHCR pull token missing or invalid")
	}
	digest, _, err = ghcrManifestDigest(ctx, client, manifest, token)
	return digest, err
}

func ghcrManifestDigest(ctx context.Context, client *http.Client, endpoint, bearerToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	response, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve GHCR manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && bearerToken == "" {
		return "", response.Header.Get("WWW-Authenticate"), errors.New("GHCR manifest requires anonymous pull token")
	}
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GHCR manifest verification failed (HTTP %d); image tag unavailable or private", response.StatusCode)
	}
	digest := strings.ToLower(strings.TrimSpace(response.Header.Get("Docker-Content-Digest")))
	if !ValidImageDigest(digest) {
		return "", "", errors.New("GHCR response omitted a valid SHA-256 manifest digest")
	}
	return digest, "", nil
}
