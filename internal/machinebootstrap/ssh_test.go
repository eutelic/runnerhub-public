package machinebootstrap

import (
	"strings"
	"testing"
)

func TestStartScriptRecoversStaleTokenPath(t *testing.T) {
	req := Request{
		HostName:        "compute",
		ControlPlaneURL: "http://runnerhub.example:8788",
		AgentImage:      "ghcr.io/eutelic/runnerhub-agent:latest",
	}
	script := startScript(req, "token", "/tmp/runnerhub-enrollment-compute.token")
	remove := strings.Index(script, `rm -rf "$TOKEN_PATH"`)
	write := strings.Index(script, `printf '%s' "$TOKEN" > "$TOKEN_PATH"`)
	if remove < 0 || write < 0 || remove > write {
		t.Fatalf("bootstrap must remove a stale token path before recreating it:\n%s", script)
	}
	if got := cleanupScript("/tmp/runnerhub-enrollment-compute.token"); !strings.Contains(got, "rm -rf") {
		t.Fatalf("cleanup must handle a stale bind-mount directory: %q", got)
	}
}

func TestBootstrapScriptSupportsPasswordedSudo(t *testing.T) {
	req := Request{
		SudoPassword:    "example-sudo-password",
		HostName:        "compute",
		ControlPlaneURL: "http://runnerhub.example:8788",
		AgentImage:      "ghcr.io/eutelic/runnerhub-agent:latest",
		InstallDocker:   true,
	}
	script := startScript(req, "token", "/tmp/token")
	for _, want := range []string{
		`SUDO_PASSWORD='example-sudo-password'`,
		`sudo -S -p '' -- "$@"`,
		"sudo_cmd apt-get update",
		"sudo_cmd apt-get install -y docker.io",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bootstrap script missing %q:\n%s", want, script)
		}
	}
}

func TestBootstrapRegistryCredentialsUseTemporaryDockerConfig(t *testing.T) {
	req := Request{
		RegistryUsername: "user",
		RegistryPassword: "example-registry-token",
		HostName:         "compute",
		ControlPlaneURL:  "http://runnerhub.example:8788",
		AgentImage:       "ghcr.io/eutelic/runnerhub-agent:latest",
	}
	script := startScript(req, "token", "/tmp/token")
	for _, want := range []string{
		`REGISTRY='ghcr.io'`,
		`REGISTRY_USER='user'`,
		`REGISTRY_PASSWORD='example-registry-token'`,
		"--password-stdin",
		"mktemp -d /tmp/runnerhub-docker-config.",
		`docker_cmd --config "$cfg" pull "$IMAGE"`,
		`rm -rf "$cfg"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bootstrap script missing %q:\n%s", want, script)
		}
	}
}

func TestTailscaleControlPlaneAddsMagicDNSResolverToAgent(t *testing.T) {
	req := Request{
		HostName:        "compute",
		ControlPlaneURL: "http://truenas-scale.example.ts.net:8788",
		AgentImage:      "ghcr.io/eutelic/runnerhub-agent:latest",
	}
	for name, script := range map[string]string{
		"start":    startScript(req, "token", "/tmp/token"),
		"finalize": finalizeScript(req, "/tmp/token"),
	} {
		if !strings.Contains(script, `AGENT_DNS='100.100.100.100'`) {
			t.Fatalf("%s script does not select Tailscale MagicDNS resolver:\n%s", name, script)
		}
		if !strings.Contains(script, `--dns=$AGENT_DNS`) {
			t.Fatalf("%s script does not pass resolver to agent container:\n%s", name, script)
		}
	}
}

func TestBootstrapCanAdvertisePublisherSupport(t *testing.T) {
	req := Request{
		HostName:               "compute",
		ControlPlaneURL:        "http://runnerhub.example:8788",
		AgentImage:             "ghcr.io/eutelic/runnerhub-agent:latest",
		AllowPrivilegedWorkers: true,
	}
	for name, script := range map[string]string{
		"start":    startScript(req, "token", "/tmp/token"),
		"finalize": finalizeScript(req, "/tmp/token"),
	} {
		if !strings.Contains(script, `PRIVILEGED=true`) {
			t.Fatalf("%s script does not enable privileged worker support:\n%s", name, script)
		}
		if !strings.Contains(script, `RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true`) {
			t.Fatalf("%s script does not pass privileged worker support to agent:\n%s", name, script)
		}
	}
}

func TestNonTailscaleControlPlaneDoesNotForceDNS(t *testing.T) {
	if got := agentDNSForControlURL("https://runnerhub.example"); got != "" {
		t.Fatalf("agentDNSForControlURL returned %q for ordinary DNS", got)
	}
}

func TestRegistryHost(t *testing.T) {
	tests := map[string]string{
		"ghcr.io/eutelic/runnerhub-agent:latest": "ghcr.io",
		"registry.example:5000/team/agent:latest": "registry.example:5000",
		"runnerhub-agent:latest":                  "docker.io",
	}
	for image, want := range tests {
		if got := registryHost(image); got != want {
			t.Fatalf("registryHost(%q)=%q, want %q", image, got, want)
		}
	}
}
