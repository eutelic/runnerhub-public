package main

import (
	"context"
	"io"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eutelic/runnerhub/internal/deployer"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	policy, err := deployer.LoadPolicy(mustEnv("RUNNERHUB_DEPLOY_POLICY_FILE"))
	must(err)
	apiKeyBytes, err := readPrivateSecret(mustEnv("RUNNERHUB_TRUENAS_API_KEY_FILE"))
	must(err)
	client, err := deployer.NewTrueNASClient(deployer.TrueNASConfig{
		Endpoint:           mustEnv("RUNNERHUB_TRUENAS_URL"),
		Username:           mustEnv("RUNNERHUB_TRUENAS_USERNAME"),
		APIKey:             strings.TrimSpace(string(apiKeyBytes)),
		CAFile:             strings.TrimSpace(os.Getenv("RUNNERHUB_TRUENAS_CA_FILE")),
		InsecureSkipVerify: boolEnv("RUNNERHUB_TRUENAS_TLS_INSECURE_SKIP_VERIFY", false),
	})
	must(err)

	maintenanceToken := ""
	if path := strings.TrimSpace(os.Getenv("RUNNERHUB_MAINTENANCE_TOKEN_FILE")); path != "" {
		value, readErr := readPrivateSecret(path)
		must(readErr)
		maintenanceToken = strings.TrimSpace(string(value))
	}
	svc := &deployer.Service{
		BaseContext:      ctx,
		Policy:           policy,
		TrueNAS:          client,
		AuditPath:        env("RUNNERHUB_DEPLOY_AUDIT_FILE", "/data/deployments.jsonl"),
		MaintenanceToken: maintenanceToken,
		ManagementKey: []byte(strings.TrimSpace(string(apiKeyBytes))),
		ManagedAppsPath: env("RUNNERHUB_MANAGED_APPS_FILE", "/data/managed-apps.json"),
	}
	server := &http.Server{
		Addr:              env("RUNNERHUB_DEPLOY_HTTP_ADDR", "0.0.0.0:8080"),
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		// Close the operation gate before draining HTTP handlers. A request
		// racing shutdown either owns a tracked operation or receives 503.
		svc.StopSelfRedeployAdmissions()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("deployer HTTP shutdown: %v", err)
			_ = server.Close()
		}
	}()
	log.Printf("runnerhub-deployer %s listening on %s", version, server.Addr)
	serveErr := server.ListenAndServe()
	stop()
	<-shutdownDone

	// A pre-redeploy cancellation gets up to 15 seconds to restore admissions.
	// Keep the process alive long enough for that bounded recovery to finish.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := svc.WaitForSelfRedeploys(waitCtx); err != nil {
		log.Printf("timed out waiting for self-redeployment recovery during shutdown: %v", err)
	}
	waitCancel()
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatal(serveErr)
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func mustEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		log.Fatalf("%s is required", key)
	}
	return value
}

func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Fatalf("%s must be true or false", key)
	}
	return parsed
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}


// readPrivateSecret accepts only a private regular file at a configured absolute
// path. Secret paths come from trusted local service configuration, never HTTP
// or workflow input.
func readPrivateSecret(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("secret file path must be absolute and clean")
	}
	// #nosec G703 -- This trusted environment-configured path is validated
	// absolute/clean; no request or workflow input controls it.
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect secret file: %w", err)
	}
	// #nosec G703 -- The same validated local configuration path is opened only
	// after Lstat verifies it is a private regular file.
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened secret file: %w", err)
	}
	if err := validatePrivateSecretFileInfo(pathInfo, openedInfo); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	return data, nil
}

func validatePrivateSecretFileInfo(pathInfo, openedInfo os.FileInfo) error {
	if pathInfo == nil || openedInfo == nil ||
		!pathInfo.Mode().IsRegular() || !openedInfo.Mode().IsRegular() ||
		!os.SameFile(pathInfo, openedInfo) ||
		pathInfo.Mode().Perm()&0o077 != 0 || openedInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("secret file must remain a private regular file")
	}
	return nil
}
