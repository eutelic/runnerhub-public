package main

import (
	"context"
	"github.com/example-user/runnerhub/internal/agenthub"
	apihttp "github.com/example-user/runnerhub/internal/api"
	"github.com/example-user/runnerhub/internal/auth"
	cachepkg "github.com/example-user/runnerhub/internal/cache"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/githubrunner"
	"github.com/example-user/runnerhub/internal/policy"
	"github.com/example-user/runnerhub/internal/reconcile"
	"github.com/example-user/runnerhub/internal/scheduler"
	"github.com/example-user/runnerhub/internal/store"
	"github.com/example-user/runnerhub/internal/telemetry"
	webui "github.com/example-user/runnerhub/web"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dbPath := env("RUNNERHUB_DB", "runnerhub.db")
	st, err := store.OpenSQLite(dbPath)
	must(err)
	defer st.Close()
	enroll := os.Getenv("RUNNERHUB_ENROLLMENT_TOKEN")
	agentSecret := os.Getenv("RUNNERHUB_AGENT_SECRET")
	if enroll == "" || agentSecret == "" {
		log.Fatal("RUNNERHUB_ENROLLMENT_TOKEN and RUNNERHUB_AGENT_SECRET are required")
	}
	hub, err := agenthub.New(st, enroll, agentSecret)
	must(err)
	metrics := &telemetry.Metrics{}
	var gh githubrunner.Backend
	var catalog githubrunner.Catalog
	if issuer := os.Getenv("RUNNERHUB_GITHUB_APP_ISSUER"); issuer != "" {
		keyPath := os.Getenv("RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE")
		key, err := os.ReadFile(keyPath)
		must(err)
		tokens, err := githubrunner.NewGitHubAppTokenSource(githubrunner.GitHubAppConfig{Issuer: issuer, PrivateKeyPEM: key})
		must(err)
		gh, err = githubrunner.NewHTTPBackend(nil, tokens)
		must(err)
		catalog, err = githubrunner.NewHTTPCatalog(nil, tokens, "")
		must(err)
	}
	allowed := map[string]struct{}{}
	for _, x := range strings.Split(env("RUNNERHUB_ALLOWED_IMAGES", "ghcr.io/actions/actions-runner:latest"), ",") {
		allowed[strings.TrimSpace(x)] = struct{}{}
	}
	var cacheStore *cachepkg.Store
	if root := os.Getenv("RUNNERHUB_CACHE_DIR"); root != "" {
		cacheStore = &cachepkg.Store{Root: root}
	}
	authm := auth.New(auth.Config{APIKeys: auth.ParseKeys(os.Getenv("RUNNERHUB_API_KEYS")), TrustOIDCHeaders: env("RUNNERHUB_TRUST_OIDC_HEADERS", "false") == "true"})
	svc := &apihttp.Service{Store: st, GitHub: gh, Catalog: catalog, Hub: hub, Scheduler: scheduler.Scheduler{Store: st}, Policy: policy.Engine{AllowedImages: allowed, MaxResources: core.Resources{CPUMillis: 64000, MemoryBytes: 256 << 30}, RequirePinnedImages: env("RUNNERHUB_REQUIRE_PINNED_IMAGES", "false") == "true"}, Metrics: metrics, Auth: authm, Cache: cacheStore, Static: webui.Handler()}
	hub.SetResultHandler(svc.HandleAgentResult)
	rec := &reconcile.Reconciler{Store: st, GitHub: gh, Hub: hub, Interval: 15 * time.Second}
	go func() {
		if err := rec.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reconciler stopped: %v", err)
		}
	}()
	addr := env("RUNNERHUB_HTTP_ADDR", "127.0.0.1:8080")
	server := &http.Server{Addr: addr, Handler: svc.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("runnerhub %s listening on %s", version, addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
