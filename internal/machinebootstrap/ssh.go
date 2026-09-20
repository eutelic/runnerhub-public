package machinebootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

type Request struct {
	Address           string
	User              string
	Password          string
	PrivateKey        string
	HostName          string
	ControlPlaneURL   string
	AgentImage        string
	InstallDocker     bool
	AllowInsecureHTTP bool
}

type WaitForHost func(context.Context, string) (core.Host, error)

type Bootstrapper interface {
	Bootstrap(context.Context, Request, string, WaitForHost) (core.Host, error)
}

type SSH struct {
	SSHBinary     string
	SSHPassBinary string
}

var safeUser = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var safeHost = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func (s SSH) Bootstrap(ctx context.Context, req Request, enrollmentToken string, wait WaitForHost) (core.Host, error) {
	if wait == nil {
		return core.Host{}, errors.New("host enrollment waiter is required")
	}
	req.Address = strings.TrimSpace(req.Address)
	req.User = strings.TrimSpace(req.User)
	req.HostName = strings.TrimSpace(req.HostName)
	req.ControlPlaneURL = strings.TrimSpace(req.ControlPlaneURL)
	req.AgentImage = strings.TrimSpace(req.AgentImage)
	if req.Address == "" || !safeHost.MatchString(req.Address) {
		return core.Host{}, errors.New("valid machine address is required")
	}
	if req.User == "" || !safeUser.MatchString(req.User) {
		return core.Host{}, errors.New("valid SSH user is required")
	}
	if req.HostName == "" {
		return core.Host{}, errors.New("host name is required")
	}
	if req.ControlPlaneURL == "" || req.AgentImage == "" || enrollmentToken == "" {
		return core.Host{}, errors.New("bootstrap control URL, agent image, and enrollment token are required")
	}

	sshBin := s.SSHBinary
	if sshBin == "" {
		sshBin = "ssh"
	}
	passBin := s.SSHPassBinary
	if passBin == "" {
		passBin = "sshpass"
	}
	if _, err := exec.LookPath(sshBin); err != nil {
		return core.Host{}, fmt.Errorf("SSH client unavailable: %w", err)
	}

	tmp, err := os.MkdirTemp("", "runnerhub-ssh-*")
	if err != nil {
		return core.Host{}, err
	}
	defer os.RemoveAll(tmp)

	knownHosts := filepath.Join(tmp, "known_hosts")
	keyPath := ""
	if strings.TrimSpace(req.PrivateKey) != "" {
		keyPath = filepath.Join(tmp, "id")
		if err := os.WriteFile(keyPath, []byte(req.PrivateKey), 0o600); err != nil {
			return core.Host{}, err
		}
	}

	run := func(command string) error {
		args, env, err := sshCommand(req, sshBin, passBin, knownHosts, keyPath)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader(command)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if len(msg) > 1024 {
				msg = msg[len(msg)-1024:]
			}
			if msg != "" {
				return fmt.Errorf("remote bootstrap failed: %s: %w", msg, err)
			}
			return fmt.Errorf("remote bootstrap failed: %w", err)
		}
		return nil
	}

	tokenPath := "/tmp/runnerhub-enrollment-" + safeName(req.HostName) + ".token"
	if err := run(startScript(req, enrollmentToken, tokenPath)); err != nil {
		return core.Host{}, err
	}
	host, err := wait(ctx, req.HostName)
	if err != nil {
		_ = run(cleanupScript(tokenPath))
		return core.Host{}, err
	}
	if err := run(finalizeScript(req, tokenPath)); err != nil {
		return host, fmt.Errorf("host enrolled but enrollment-secret cleanup failed: %w", err)
	}
	return host, nil
}

func sshCommand(req Request, sshBin, passBin, knownHosts, keyPath string) ([]string, []string, error) {
	host, port, err := splitAddress(req.Address)
	if err != nil {
		return nil, nil, err
	}
	args := []string{sshBin,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-p", port,
	}
	if keyPath != "" {
		args = append(args, "-i", keyPath, "-o", "IdentitiesOnly=yes")
	}
	env := []string{}
	if req.Password != "" {
		if _, err := exec.LookPath(passBin); err != nil {
			return nil, nil, fmt.Errorf("password SSH requires sshpass: %w", err)
		}
		args = append([]string{passBin, "-e"}, args...)
		env = append(env, "SSHPASS="+req.Password)
	} else if keyPath == "" {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, req.User+"@"+host, "sh", "-s")
	return args, env, nil
}

func splitAddress(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") || strings.Count(value, ":") == 1 {
		if host, port, err := net.SplitHostPort(value); err == nil {
			if host == "" || port == "" {
				return "", "", errors.New("invalid SSH address")
			}
			return host, port, nil
		}
	}
	if value == "" || strings.ContainsAny(value, " /\\\t\r\n") {
		return "", "", errors.New("invalid SSH address")
	}
	return value, "22", nil
}

func startScript(req Request, token, tokenPath string) string {
	return commonScript(req) + "\n" +
		"TOKEN=" + shellQuote(token) + "\n" +
		"TOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`ensure_docker
umask 077
printf '%s' "$TOKEN" > "$TOKEN_PATH"
docker_cmd pull "$IMAGE" >/dev/null
docker_cmd rm -f runnerhub-agent >/dev/null 2>&1 || true
args="run -d --name runnerhub-agent --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/runnerhub:/var/lib/runnerhub -v $TOKEN_PATH:/run/runnerhub-enrollment-token:ro -e RUNNERHUB_CONTROL_PLANE_URL=$CONTROL -e RUNNERHUB_ENROLLMENT_TOKEN_FILE=/run/runnerhub-enrollment-token -e RUNNERHUB_HOST_NAME=$HOST_NAME -e RUNNERHUB_PROVIDER=docker"
if [ "$INSECURE" = true ]; then args="$args -e RUNNERHUB_ALLOW_INSECURE_HTTP=true"; fi
# Values are validated/quoted before reaching this shell; use positional expansion only for fixed option shapes.
docker_cmd $args "$IMAGE" >/dev/null
`
}

func finalizeScript(req Request, tokenPath string) string {
	return commonScript(req) + "\nTOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`ensure_docker
docker_cmd rm -f runnerhub-agent >/dev/null 2>&1 || true
args="run -d --name runnerhub-agent --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/runnerhub:/var/lib/runnerhub -e RUNNERHUB_CONTROL_PLANE_URL=$CONTROL -e RUNNERHUB_HOST_NAME=$HOST_NAME -e RUNNERHUB_PROVIDER=docker"
if [ "$INSECURE" = true ]; then args="$args -e RUNNERHUB_ALLOW_INSECURE_HTTP=true"; fi
docker_cmd $args "$IMAGE" >/dev/null
rm -f "$TOKEN_PATH"
`
}

func cleanupScript(tokenPath string) string {
	return "rm -f " + shellQuote(tokenPath) + "\n"
}

func commonScript(req Request) string {
	install := "false"
	if req.InstallDocker {
		install = "true"
	}
	insecure := "false"
	if req.AllowInsecureHTTP {
		insecure = "true"
	}
	return `set -eu
CONTROL=` + shellQuote(req.ControlPlaneURL) + `
IMAGE=` + shellQuote(req.AgentImage) + `
HOST_NAME=` + shellQuote(req.HostName) + `
INSTALL_DOCKER=` + install + `
INSECURE=` + insecure + `
docker_cmd() {
  if docker info >/dev/null 2>&1; then docker "$@"; return; fi
  if command -v sudo >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then sudo -n docker "$@"; return; fi
  echo "Docker is installed but this SSH user cannot access the daemon" >&2
  exit 43
}
ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    if [ "$INSTALL_DOCKER" != true ]; then echo "Docker is not installed" >&2; exit 42; fi
    if ! command -v sudo >/dev/null 2>&1; then echo "Docker installation requires sudo" >&2; exit 42; fi
    if command -v apt-get >/dev/null 2>&1; then
      sudo -n apt-get update
      sudo -n apt-get install -y docker.io
    elif command -v dnf >/dev/null 2>&1; then
      sudo -n dnf install -y docker
    elif command -v yum >/dev/null 2>&1; then
      sudo -n yum install -y docker
    elif command -v pacman >/dev/null 2>&1; then
      sudo -n pacman -Sy --noconfirm docker
    else
      echo "Unsupported package manager; install Docker manually" >&2
      exit 42
    fi
    if command -v systemctl >/dev/null 2>&1; then sudo -n systemctl enable --now docker; fi
  fi
  docker_cmd version >/dev/null
}
`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func safeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "host"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func WaitTimeout(parent context.Context, timeout time.Duration, fn func() (core.Host, bool, error)) (core.Host, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		host, ok, err := fn()
		if err != nil {
			return core.Host{}, err
		}
		if ok {
			return host, nil
		}
		select {
		case <-ctx.Done():
			return core.Host{}, fmt.Errorf("timed out waiting for RunnerHub agent enrollment: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
