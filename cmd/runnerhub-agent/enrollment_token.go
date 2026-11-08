package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readEnrollmentToken accepts a file supplied only by the trusted local agent
// configuration. It is never a user-supplied API request path. We validate its
// shape and reject symbolic links or world-readable files before using its
// contents as a short-lived bearer credential.
func readEnrollmentToken() (string, error) {
	path := strings.TrimSpace(os.Getenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE"))
	if path == "" {
		return strings.TrimSpace(os.Getenv("RUNNERHUB_ENROLLMENT_TOKEN")), nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("agent enrollment token file must be an absolute, clean path")
	}
	// #nosec G703 -- Trusted local service configuration path is validated
	// absolute and clean; no remote request or workflow input controls it.
	stat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect agent enrollment token file: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0o077 != 0 {
		return "", errors.New("agent enrollment token file must be a private, regular file")
	}
	// #nosec G703 -- Path comes exclusively from a trusted local environment
	// configuration; validated absolute/clean and Lstat-checked before reading.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read agent enrollment token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("agent enrollment token file is empty")
	}
	return token, nil
}
