package machinebootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

type Request struct {
	Address           string
	User              string
	Password          string
	SudoPassword      string
	PrivateKey        string
	HostKeyFingerprint string
	RegistryUsername  string
	RegistryPassword  string
	HostName          string
	ControlPlaneURL   string
	AgentImage              string
	AgentUpdaterImage string
	InstallDocker           bool
	AllowInsecureHTTP       bool
	AllowPrivilegedWorkers  bool
	AndroidEmulator bool
}

type WaitForHost func(context.Context, string) (core.Host, error)

type Bootstrapper interface {
	Bootstrap(context.Context, Request, string, WaitForHost) (core.Host, error)
}

type HostKeyScanner interface {
	ScanHostKey(context.Context, string) ([]string, error)
}

type SSH struct {
	SSHBinary     string
	SSHPassBinary string
}

var safeUser = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var pinnedUpdaterImage = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent-updater@sha256:[a-f0-9]{64}$`)
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
	req.AgentUpdaterImage = strings.TrimSpace(req.AgentUpdaterImage)
	if req.AgentUpdaterImage!="" && !pinnedUpdaterImage.MatchString(req.AgentUpdaterImage){return core.Host{},errors.New("updater image must be the official pinned digest")}
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
	if strings.TrimSpace(req.HostKeyFingerprint) != "" {
		reportProgress(ctx, "verifying-host-key")
		if err := pinHostKey(ctx, req.Address, req.HostKeyFingerprint, knownHosts); err != nil {
			return core.Host{}, err
		}
	}
	keyPath := ""
	if strings.TrimSpace(req.PrivateKey) != "" {
		keyPath = filepath.Join(tmp, "id")
		if err := os.WriteFile(keyPath, []byte(req.PrivateKey), 0o600); err != nil {
			return core.Host{}, err
		}
	}

	runOutput := func(command string) (string, error) {
		args, env, err := sshCommand(req, sshBin, passBin, knownHosts, keyPath)
		if err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader(command)
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if len(msg) > 2048 {
			msg = msg[len(msg)-2048:]
		}
		if err != nil {
			if msg != "" {
				return msg, fmt.Errorf("remote bootstrap failed: %s: %w", msg, err)
			}
			return "", fmt.Errorf("remote bootstrap failed: %w", err)
		}
		return msg, nil
	}
	run := func(command string) error {
		_, err := runOutput(command)
		return err
	}

	tokenPath := "/tmp/runnerhub-enrollment-" + safeName(req.HostName) + ".token"
	start, finalize, diagnostics, cleanup := startScript(req, enrollmentToken, tokenPath), finalizeScript(req, tokenPath), diagnosticsScript(req), cleanupScript(tokenPath)
	if req.AndroidEmulator {
		if req.AllowPrivilegedWorkers {return core.Host{}, errors.New("native Android agent cannot enable publisher/DinD privileges")}
		start = startAndroidNativeScript(req,enrollmentToken,tokenPath)
		finalize = finalizeAndroidNativeScript(req,tokenPath)
		diagnostics = diagnosticsAndroidNativeScript(req)
		cleanup = cleanupAndroidNativeScript(req,tokenPath)
	}
	reportProgress(ctx,"configuring-remote-host")
	if err:=run(start);err!=nil{
		if req.AndroidEmulator{_ = run(cleanup)}
		return core.Host{},err
	}
	reportProgress(ctx,"waiting-for-agent")
	host,err:=wait(ctx,req.HostName)
	if err!=nil{
		output,_:=runOutput(diagnostics)
		_ = run(cleanup)
		if output!=""{return core.Host{},fmt.Errorf("%w; agent diagnostics: %s",err,output)}
		return core.Host{},err
	}
	reportProgress(ctx,"cleaning-enrollment-secret")
	if err:=run(finalize);err!=nil{return host,fmt.Errorf("host enrolled but enrollment-secret cleanup failed: %w",err)}
	return host, nil
}

func sshCommand(req Request, sshBin, passBin, knownHosts, keyPath string) ([]string, []string, error) {
	host, port, err := splitAddress(req.Address)
	if err != nil {
		return nil, nil, err
	}
	strictHostKeyChecking := "accept-new"
	if strings.TrimSpace(req.HostKeyFingerprint) != "" {
		strictHostKeyChecking = "yes"
	}
	args := []string{sshBin,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "StrictHostKeyChecking=" + strictHostKeyChecking,
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

func (s SSH) ScanHostKey(ctx context.Context, address string) ([]string, error) {
	tmp, err := os.MkdirTemp("", "runnerhub-hostkey-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	return scanHostKeys(ctx, address, filepath.Join(tmp, "known_hosts"))
}

func pinHostKey(ctx context.Context, address, expected, knownHosts string) error {
	expected = strings.TrimSpace(expected)
	if !strings.HasPrefix(expected, "SHA256:") || strings.ContainsAny(expected, " \t\r\n") {
		return errors.New("SSH host key fingerprint must use SHA256:<fingerprint> format")
	}
	observed, err := scanHostKeys(ctx, address, knownHosts)
	if err != nil {
		return err
	}
	if !slices.Contains(observed, expected) {
		return fmt.Errorf("SSH host key fingerprint mismatch: expected %s; observed %s", expected, strings.Join(observed, ", "))
	}
	return retainPinnedHostKeys(ctx, knownHosts, expected)
}

func retainPinnedHostKeys(ctx context.Context, knownHosts, expected string) error {
	keygenBin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return fmt.Errorf("SSH host-key pinning requires ssh-keygen: %w", err)
	}
	data, err := os.ReadFile(knownHosts)
	if err != nil {
		return err
	}
	candidate := knownHosts + ".candidate"
	defer os.Remove(candidate)
	matched := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := os.WriteFile(candidate, []byte(line+"\n"), 0o600); err != nil { // #nosec G703 -- candidate is derived from RunnerHub's private os.MkdirTemp known_hosts path, not a user-selected filesystem path.
			return err
		}
		out, err := exec.CommandContext(ctx, keygenBin, "-l", "-f", candidate, "-E", "sha256").Output()
		if err != nil {
			continue
		}
		if strings.Contains(" "+string(out)+" ", " "+expected+" ") {
			matched = append(matched, line)
		}
	}
	if len(matched) == 0 {
		return errors.New("verified SSH fingerprint could not be mapped back to a host key")
	}
	return os.WriteFile(knownHosts, []byte(strings.Join(matched, "\n")+"\n"), 0o600) // #nosec G703 -- knownHosts is created by RunnerHub inside a private os.MkdirTemp directory before this helper is called.
}

func scanHostKeys(ctx context.Context, address, knownHosts string) ([]string, error) {
	host, port, err := splitAddress(address)
	if err != nil {
		return nil, err
	}
	scanBin, err := exec.LookPath("ssh-keyscan")
	if err != nil {
		return nil, fmt.Errorf("SSH host-key scan requires ssh-keyscan: %w", err)
	}
	keygenBin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return nil, fmt.Errorf("SSH host-key scan requires ssh-keygen: %w", err)
	}
	scan := exec.CommandContext(ctx, scanBin, "-T", "10", "-p", port, host)
	out, err := scan.Output()
	if err != nil {
		return nil, fmt.Errorf("scan SSH host key: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, errors.New("SSH host-key scan returned no keys")
	}
	if err := os.WriteFile(knownHosts, out, 0o600); err != nil {
		return nil, err
	}
	fingerprintOut, err := exec.CommandContext(ctx, keygenBin, "-l", "-f", knownHosts, "-E", "sha256").Output()
	if err != nil {
		return nil, fmt.Errorf("fingerprint SSH host key: %w", err)
	}
	observed := []string{}
	for _, line := range strings.Split(string(fingerprintOut), "\n") {
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "SHA256:") && !slices.Contains(observed, field) {
				observed = append(observed, field)
			}
		}
	}
	if len(observed) == 0 {
		return nil, errors.New("SSH host-key scan produced no SHA256 fingerprints")
	}
	return observed, nil
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
rm -rf "$TOKEN_PATH"
umask 077
printf '%s' "$TOKEN" > "$TOKEN_PATH"
pull_agent_image
docker_cmd rm -f runnerhub-agent >/dev/null 2>&1 || true
args="run -d --name runnerhub-agent --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/runnerhub:/var/lib/runnerhub -v $TOKEN_PATH:/run/runnerhub-enrollment-token:ro -e RUNNERHUB_CONTROL_PLANE_URL=$CONTROL -e RUNNERHUB_ENROLLMENT_TOKEN_FILE=/run/runnerhub-enrollment-token -e RUNNERHUB_HOST_NAME=$HOST_NAME -e RUNNERHUB_PROVIDER=docker"
if [ -n "$UPDATER_IMAGE" ]; then args="$args -e RUNNERHUB_AGENT_UPDATER_IMAGE=$UPDATER_IMAGE"; fi
if [ "$INSECURE" = true ]; then args="$args -e RUNNERHUB_ALLOW_INSECURE_HTTP=true"; fi
if [ "$PRIVILEGED" = true ]; then args="$args -e RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true -e RUNNERHUB_CGROUP_ROOT=/sys/fs/cgroup-host --cgroupns=host --cap-add SYS_ADMIN -v /sys/fs/cgroup:/sys/fs/cgroup-host:rw"; fi
if [ -n "$AGENT_DNS" ]; then args="$args --dns=$AGENT_DNS"; fi
# Values are validated/quoted before reaching this shell; use positional expansion only for fixed option shapes.
docker_cmd $args "$IMAGE" >/dev/null
`
}

func finalizeScript(req Request, tokenPath string) string {
	req.RegistryUsername = ""
	req.RegistryPassword = ""
	return commonScript(req) + "\nTOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`ensure_docker
docker_cmd rm -f runnerhub-agent >/dev/null 2>&1 || true
args="run -d --name runnerhub-agent --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/runnerhub:/var/lib/runnerhub -e RUNNERHUB_CONTROL_PLANE_URL=$CONTROL -e RUNNERHUB_HOST_NAME=$HOST_NAME -e RUNNERHUB_PROVIDER=docker"
if [ -n "$UPDATER_IMAGE" ]; then args="$args -e RUNNERHUB_AGENT_UPDATER_IMAGE=$UPDATER_IMAGE"; fi
if [ "$INSECURE" = true ]; then args="$args -e RUNNERHUB_ALLOW_INSECURE_HTTP=true"; fi
if [ "$PRIVILEGED" = true ]; then args="$args -e RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true -e RUNNERHUB_CGROUP_ROOT=/sys/fs/cgroup-host --cgroupns=host --cap-add SYS_ADMIN -v /sys/fs/cgroup:/sys/fs/cgroup-host:rw"; fi
if [ -n "$AGENT_DNS" ]; then args="$args --dns=$AGENT_DNS"; fi
docker_cmd $args "$IMAGE" >/dev/null
rm -rf "$TOKEN_PATH"
`
}

func cleanupScript(tokenPath string) string {
	return "rm -rf " + shellQuote(tokenPath) + "\n"
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
	privileged := "false"
	if req.AllowPrivilegedWorkers {
		privileged = "true"
	}
	registry := registryHost(req.AgentImage)
	agentDNS := agentDNSForControlURL(req.ControlPlaneURL)
	return `set -eu
CONTROL=` + shellQuote(req.ControlPlaneURL) + `
IMAGE=` + shellQuote(req.AgentImage) + `
UPDATER_IMAGE=` + shellQuote(req.AgentUpdaterImage) + `
HOST_NAME=` + shellQuote(req.HostName) + `
SUDO_PASSWORD=` + shellQuote(req.SudoPassword) + `
REGISTRY=` + shellQuote(registry) + `
REGISTRY_USER=` + shellQuote(req.RegistryUsername) + `
REGISTRY_PASSWORD=` + shellQuote(req.RegistryPassword) + `
AGENT_DNS=` + shellQuote(agentDNS) + `
INSTALL_DOCKER=` + install + `
INSECURE=` + insecure + `
PRIVILEGED=` + privileged + `
sudo_try() {
  if sudo -n "$@" >/dev/null 2>&1; then return 0; fi
  if [ -n "$SUDO_PASSWORD" ]; then
    printf '%s\n' "$SUDO_PASSWORD" | sudo -S -p '' -- "$@"
    return $?
  fi
  return 1
}
sudo_cmd() {
  if sudo -n true >/dev/null 2>&1; then sudo -n "$@"; return; fi
  if [ -n "$SUDO_PASSWORD" ]; then
    printf '%s\n' "$SUDO_PASSWORD" | sudo -S -p '' -- "$@"
    return
  fi
  echo "sudo requires authentication; provide a sudo password in Add machine" >&2
  exit 44
}
docker_cmd() {
  if docker info >/dev/null 2>&1; then docker "$@"; return; fi
  if command -v sudo >/dev/null 2>&1 && sudo_try docker info >/dev/null 2>&1; then sudo_cmd docker "$@"; return; fi
  echo "Docker is installed but this SSH user cannot access the daemon" >&2
  exit 43
}
ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    if [ "$INSTALL_DOCKER" != true ]; then echo "Docker is not installed" >&2; exit 42; fi
    if ! command -v sudo >/dev/null 2>&1; then echo "Docker installation requires sudo" >&2; exit 42; fi
    if command -v apt-get >/dev/null 2>&1; then
      sudo_cmd apt-get update
      sudo_cmd apt-get install -y docker.io
    elif command -v dnf >/dev/null 2>&1; then
      sudo_cmd dnf install -y docker
    elif command -v yum >/dev/null 2>&1; then
      sudo_cmd yum install -y docker
    elif command -v pacman >/dev/null 2>&1; then
      sudo_cmd pacman -Sy --noconfirm docker
    else
      echo "Unsupported package manager; install Docker manually" >&2
      exit 42
    fi
    if command -v systemctl >/dev/null 2>&1; then sudo_cmd systemctl enable --now docker; fi
  fi
  docker_cmd version >/dev/null
}
pull_agent_image() {
  if [ -n "$REGISTRY_PASSWORD" ]; then
    if [ -z "$REGISTRY_USER" ]; then
      echo "registry username is required when a registry token/password is provided" >&2
      exit 45
    fi
    cfg="$(mktemp -d /tmp/runnerhub-docker-config.XXXXXX)"
    chmod 700 "$cfg"
    trap 'rm -rf "$cfg"' 0 1 2 15
    if ! printf '%s' "$REGISTRY_PASSWORD" | docker_cmd --config "$cfg" login "$REGISTRY" -u "$REGISTRY_USER" --password-stdin >/dev/null; then
      echo "RunnerHub agent registry authentication failed for $REGISTRY" >&2
      exit 45
    fi
    if ! docker_cmd --config "$cfg" pull "$IMAGE" >/dev/null; then
      echo "RunnerHub agent image pull failed for $IMAGE after registry authentication" >&2
      exit 46
    fi
    rm -rf "$cfg"
    trap - 0 1 2 15
    return
  fi
  if ! docker_cmd pull "$IMAGE" >/dev/null; then
    echo "RunnerHub agent image pull failed for $IMAGE; if the image is private, provide registry credentials in Add machine" >&2
    exit 46
  fi
}
`
}

func diagnosticsScript(req Request) string {
	req.RegistryUsername = ""
	req.RegistryPassword = ""
	return commonScript(req) + `
ensure_docker
docker_cmd logs --tail 40 runnerhub-agent 2>&1 || true
`
}

func registryHost(image string) string {
	image = strings.TrimSpace(image)
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 1 {
		return "docker.io"
	}
	first := parts[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || strings.EqualFold(first, "localhost") {
		return first
	}
	return "docker.io"
}

func agentDNSForControlURL(controlURL string) string {
	u, err := url.Parse(strings.TrimSpace(controlURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if strings.HasSuffix(host, ".ts.net") {
		return "100.100.100.100"
	}
	return ""
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
