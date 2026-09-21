package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	apiKeyBytes, err := os.ReadFile(mustEnv("RUNNERHUB_TRUENAS_API_KEY_FILE"))
	must(err)
	client, err := deployer.NewTrueNASClient(deployer.TrueNASConfig{
		Endpoint:           mustEnv("RUNNERHUB_TRUENAS_URL"),
		Username:           mustEnv("RUNNERHUB_TRUENAS_USERNAME"),
		APIKey:             strings.TrimSpace(string(apiKeyBytes)),
		CAFile:             strings.TrimSpace(os.Getenv("RUNNERHUB_TRUENAS_CA_FILE")),
		InsecureSkipVerify: boolEnv("RUNNERHUB_TRUENAS_TLS_INSECURE_SKIP_VERIFY", false),
	})
	must(err)

	svc := &deployer.Service{
		Policy:    policy,
		TrueNAS:   client,
		AuditPath: env("RUNNERHUB_DEPLOY_AUDIT_FILE", "/data/deployments.jsonl"),
	}
	server := &http.Server{
		Addr:              env("RUNNERHUB_DEPLOY_HTTP_ADDR", "0.0.0.0:8080"),
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("runnerhub-deployer %s listening on %s", version, server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
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
