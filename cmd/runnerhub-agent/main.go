package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/provider"
	dockerprovider "github.com/eutelic/runnerhub/internal/providers/docker"
	firecrackerprovider "github.com/eutelic/runnerhub/internal/providers/firecracker"
	processprovider "github.com/eutelic/runnerhub/internal/providers/process"
	proxmoxprovider "github.com/eutelic/runnerhub/internal/providers/proxmox"
	truenasprovider "github.com/eutelic/runnerhub/internal/providers/truenas"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	name := env("RUNNERHUB_HOST_NAME", hostname())
	kind := env("RUNNERHUB_PROVIDER", "docker")
	stateDir := env("RUNNERHUB_STATE_DIR", "/var/lib/runnerhub")
	providers := map[string]provider.Provider{}

	switch kind {
	case "docker":
		p, err := dockerprovider.New(dockerprovider.Config{
			HostID:                 name,
			StateDir:               stateDir,
			AllowedImages:          []string{env("RUNNERHUB_RUNNER_IMAGE", images.DefaultRunnerImage)},
			PullPolicy:             env("RUNNERHUB_DOCKER_PULL_POLICY", "always"),
			DinDImage:              env("RUNNERHUB_DIND_IMAGE", images.DefaultDinDImage),
			AllowPrivilegedWorkers: boolEnv("RUNNERHUB_ALLOW_PRIVILEGED_WORKERS", false),
		})
		must(err)
		var selected provider.Provider = p
		if boolEnv("RUNNERHUB_DIAGNOSTICS_ENABLED", false) {
			selected = newDiagnosticProvider(p, env("RUNNERHUB_DOCKER_BINARY", "docker"))
			log.Printf("worker startup diagnostics enabled")
		}
		providers[kind] = selected

	case "process":
		p, err := processprovider.New(processprovider.Config{
			HostID:    name,
			RunnerDir: mustEnv("RUNNERHUB_RUNNER_DIR"),
			StateDir:  filepath.Join(stateDir, "process"),
		})
		must(err)
		providers[kind] = p

	case "firecracker":
		p, err := firecrackerprovider.New(firecrackerprovider.Config{
			HostID:      name,
			KernelImage: mustEnv("RUNNERHUB_FIRECRACKER_KERNEL"),
			RootFS:      mustEnv("RUNNERHUB_FIRECRACKER_ROOTFS"),
			Bridge:      mustEnv("RUNNERHUB_FIRECRACKER_BRIDGE"),
			StateDir:    filepath.Join(stateDir, "firecracker"),
		})
		must(err)
		providers[kind] = p

	case "proxmox":
		p, err := proxmoxprovider.New(proxmoxprovider.Config{
			HostID:       name,
			BaseURL:      mustEnv("RUNNERHUB_PROXMOX_URL"),
			TokenID:      mustEnv("RUNNERHUB_PROXMOX_TOKEN_ID"),
			TokenSecret:  mustEnv("RUNNERHUB_PROXMOX_TOKEN_SECRET"),
			Node:         mustEnv("RUNNERHUB_PROXMOX_NODE"),
			TemplateVMID: mustIntEnv("RUNNERHUB_PROXMOX_TEMPLATE_VMID"),
			SnippetDir:   mustEnv("RUNNERHUB_PROXMOX_SNIPPET_DIR"),
			SnippetStore: env("RUNNERHUB_PROXMOX_SNIPPET_STORE", "local"),
		})
		must(err)
		providers[kind] = p

	case "truenas":
		p, err := truenasprovider.New(truenasprovider.Config{
			HostID:        name,
			Root:          mustEnv("RUNNERHUB_TRUENAS_ROOT"),
			AllowedImages: []string{env("RUNNERHUB_RUNNER_IMAGE", images.DefaultRunnerImage)},
		})
		must(err)
		providers[kind] = p

	default:
		log.Fatalf("unsupported provider %q", kind)
	}

	credentialFile := env("RUNNERHUB_CREDENTIAL_FILE", filepath.Join(stateDir, "agent-credentials.json"))
	client, err := agent.NewClient(agent.ClientConfig{
		ControlPlaneURL:   mustEnv("RUNNERHUB_CONTROL_PLANE_URL"),
		EnrollmentToken:   os.Getenv("RUNNERHUB_ENROLLMENT_TOKEN"),
		CredentialFile:    credentialFile,
		Name:              name,
		Provider:          kind,
		AgentVersion:      version,
		AllowInsecureHTTP: boolEnv("RUNNERHUB_ALLOW_INSECURE_HTTP", false),
	}, providers)
	must(err)

	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
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

func mustIntEnv(key string) int {
	value := mustEnv(key)
	n, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf("%s must be an integer", key)
	}
	return n
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

func hostname() string {
	host, _ := os.Hostname()
	return host
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
