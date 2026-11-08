package machinebootstrap

// This native process-provider bootstrap is intentionally a separate systemd
// service and Linux account. It never stops or replaces the existing
// runnerhub-agent Docker container, preserving ordinary RunnerHub CI.
func startAndroidNativeScript(req Request, token, tokenPath string) string {
	return commonScript(req) + "\nTOKEN=" + shellQuote(token) + "\nTOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`set -eu
NATIVE_USER=runnerhub-android
NATIVE_HOME=/var/lib/runnerhub-android
STATE_DIR="$NATIVE_HOME/state"
RUNNER_DIR=/usr/local/share/runnerhub-android/actions-runner
SERVICE=runnerhub-android-agent
RUNNER_VERSION=2.338.2
RUNNER_SHA=baebf204307bac6193480852732f0aba2ae97b6c5597c244f4d5558390147c40
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "Android KVM native agent requires Linux x86_64" >&2
  exit 61
fi
# Install common runtime prerequisites on Debian/Ubuntu when missing.
# This is scoped to the native setup and does not restart Docker or the
# existing RunnerHub agent.
need_runtime_tools=false
for bin in curl tar sha256sum python3 unzip; do
  if ! command -v "$bin" >/dev/null 2>&1; then need_runtime_tools=true; fi
done
if [ "$need_runtime_tools" = true ] && command -v apt-get >/dev/null 2>&1; then
  sudo_cmd apt-get update -qq
  sudo_cmd apt-get install -y --no-install-recommends curl tar coreutils python3 unzip ca-certificates
fi
for bin in curl tar sha256sum python3 unzip bash systemctl; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "Missing prerequisite $bin on native Android KVM host" >&2
    exit 61
  fi
done
# The native account is never added to docker or sudo. The SSH bootstrap user
# temporarily uses Docker only to extract our trusted agent build.
ensure_docker
if sudo_cmd systemctl is-active --quiet "$SERVICE"; then
  echo "Dedicated Android KVM agent is already running; refusing to replace it" >&2
  exit 62
fi
if ! getent group kvm >/dev/null 2>&1; then
  echo "KVM group is missing from the host" >&2
  exit 61
fi
if ! id -u "$NATIVE_USER" >/dev/null 2>&1; then
  sudo_cmd useradd --system --user-group --create-home --home-dir "$NATIVE_HOME" --shell /usr/sbin/nologin "$NATIVE_USER"
fi
sudo_cmd usermod -aG kvm "$NATIVE_USER"
for dir in "$NATIVE_HOME" "$STATE_DIR"; do
  sudo_cmd install -d -m 0700 -o "$NATIVE_USER" -g "$NATIVE_USER" "$dir"
done
# The reusable GitHub runner distribution is a root-managed trust anchor.
# Each JIT job runs in its own private copy; the native account must never
# be able to rewrite the template for subsequent jobs.
sudo_cmd install -d -m 0755 -o root -g root /usr/local/share/runnerhub-android
sudo_cmd install -d -m 0755 -o root -g root "$RUNNER_DIR"
sudo_cmd chown -R root:root "$RUNNER_DIR"
sudo_cmd chmod -R a+rX "$RUNNER_DIR"
sudo_cmd -u "$NATIVE_USER" python3 -c 'import os,fcntl,stat; s=os.stat("/dev/kvm"); assert stat.S_ISCHR(s.st_mode); fd=os.open("/dev/kvm",os.O_RDWR); assert fcntl.ioctl(fd,0xAE00,0)==12; os.close(fd)'
if [ "$(getconf _NPROCESSORS_ONLN)" -lt 4 ]; then
  echo "Android emulator needs at least 4 available CPU cores" >&2
  exit 61
fi
if ! awk '/^MemAvailable:/ { if ($2 >= 8388608) ready=1 } END { exit !ready }' /proc/meminfo; then
  echo "Android emulator needs 8 GiB available RAM" >&2
  exit 61
fi
free_kb="$(sudo_cmd -u "$NATIVE_USER" df -Pk "$NATIVE_HOME" | awk 'END {print $4}')"
if [ "$free_kb" -lt 20971520 ]; then
  echo "Android emulator needs 20 GiB available disk space" >&2
  exit 61
fi

# Pull before setting the temporary-file cleanup trap: authenticated Docker
# pulls replace shell traps while using an ephemeral registry login config.
pull_agent_image
tmp="$(mktemp -d /tmp/runnerhub-android-setup.XXXXXX)"
stage_id=""
cleanup_stage() {
  if [ -n "$stage_id" ]; then docker_cmd rm -f "$stage_id" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
}
trap cleanup_stage EXIT
installed_runner_version="$(sudo_cmd -u "$NATIVE_USER" cat "$RUNNER_DIR/.runnerhub-runner-version" 2>/dev/null || true)"
if [ "$installed_runner_version" != "$RUNNER_VERSION" ]; then
  echo "Downloading verified GitHub Actions runner $RUNNER_VERSION..."
  curl -fL --retry 3 --connect-timeout 15 \
    "https://github.com/actions/runner/releases/download/v$RUNNER_VERSION/actions-runner-linux-x64-$RUNNER_VERSION.tar.gz" \
    -o "$tmp/actions-runner.tar.gz"
  printf '%s  %s\n' "$RUNNER_SHA" "$tmp/actions-runner.tar.gz" | sha256sum -c -
  # GitHub's checksum-verified runner archive contains the dependency
  # installer. Stage that script in the trusted SSH user's private temporary
  # directory before running it as root. Never root-execute a mutable file
  # from the lower-privileged native runner account.
  mkdir -m 0700 "$tmp/runtime-deps"
  dep_path="$(tar -tzf "$tmp/actions-runner.tar.gz" | grep -E '(^|/)bin/installdependencies[.]sh$' | head -n 1)"
  if [ -z "$dep_path" ]; then echo "Pinned runner has no dependency installer" >&2; exit 61; fi
  tar -xzf "$tmp/actions-runner.tar.gz" -C "$tmp/runtime-deps" "$dep_path"
  sudo_cmd bash "$tmp/runtime-deps/$dep_path"
  sudo_cmd tar -xzf "$tmp/actions-runner.tar.gz" -C "$RUNNER_DIR" --no-same-owner
  sudo_cmd chown -R root:root "$RUNNER_DIR"
  sudo_cmd chmod -R a+rX "$RUNNER_DIR"
  printf '%s\n' "$RUNNER_VERSION" > "$tmp/runner-version"
  sudo_cmd install -m 0644 -o root -g root "$tmp/runner-version" "$RUNNER_DIR/.runnerhub-runner-version"
fi
sudo_cmd -u "$NATIVE_USER" test -x "$RUNNER_DIR/run.sh"
echo "Extracting RunnerHub agent from published image..."
stage_id="$(docker_cmd create "$IMAGE")"
docker_cmd cp "$stage_id:/usr/local/bin/runnerhub-agent" "$tmp/runnerhub-agent"
docker_cmd rm -f "$stage_id" >/dev/null
stage_id=""
test -s "$tmp/runnerhub-agent"
sudo_cmd install -d -m 0755 /usr/local/lib/runnerhub-android
sudo_cmd install -m 0755 -o root -g root "$tmp/runnerhub-agent" /usr/local/lib/runnerhub-android/runnerhub-agent

# Set up an isolated user-owned credential file, then remove the SSH-stage copy.
rm -rf "$TOKEN_PATH"
umask 077
printf '%s' "$TOKEN" > "$TOKEN_PATH"
sudo_cmd install -m 0600 -o "$NATIVE_USER" -g "$NATIVE_USER" "$TOKEN_PATH" "$STATE_DIR/enrollment-token"
rm -f "$TOKEN_PATH"

printf '%s\n' \
  "RUNNERHUB_PROVIDER=process" \
  "RUNNERHUB_ANDROID_EMULATOR_ONLY=true" \
  "RUNNERHUB_HOST_NAME=$HOST_NAME" \
  "RUNNERHUB_RUNNER_DIR=$RUNNER_DIR" \
  "RUNNERHUB_STATE_DIR=$STATE_DIR" \
  "RUNNERHUB_CONTROL_PLANE_URL=$CONTROL" \
  "RUNNERHUB_ENROLLMENT_TOKEN_FILE=$STATE_DIR/enrollment-token" \
  "RUNNERHUB_ALLOW_INSECURE_HTTP=$INSECURE" \
  "HOME=$NATIVE_HOME" > "$tmp/agent.env"
sudo_cmd install -m 0600 -o root -g root "$tmp/agent.env" /etc/runnerhub-android-agent.env

cat > "$tmp/agent.service" <<'UNIT'
[Unit]
Description=RunnerHub dedicated native Android KVM agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=runnerhub-android
Group=runnerhub-android
SupplementaryGroups=kvm
WorkingDirectory=/var/lib/runnerhub-android
EnvironmentFile=/etc/runnerhub-android-agent.env
ExecStart=/usr/local/lib/runnerhub-android/runnerhub-agent
Restart=on-failure
RestartSec=10s
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/runnerhub-android
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true

[Install]
WantedBy=multi-user.target
UNIT
sudo_cmd install -m 0644 -o root -g root "$tmp/agent.service" /etc/systemd/system/runnerhub-android-agent.service
sudo_cmd systemctl daemon-reload
sudo_cmd systemctl enable --now "$SERVICE"
echo "Native Android KVM agent started; awaiting its first verified RunnerHub heartbeat."`
}
func finalizeAndroidNativeScript(req Request, tokenPath string) string {
	req.RegistryUsername = ""
	req.RegistryPassword = ""
	return commonScript(req) + "\nTOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`set -eu
sudo_cmd systemctl is-active --quiet runnerhub-android-agent
# Enrollment is one-time. The agent's saved rotating host credential is kept.
sudo_cmd rm -f /var/lib/runnerhub-android/state/enrollment-token
rm -f "$TOKEN_PATH"
echo "Native Android KVM host enrolled and short-lived token removed."`
}
func cleanupAndroidNativeScript(req Request, tokenPath string) string {
	req.RegistryUsername = ""
	req.RegistryPassword = ""
	return commonScript(req) + "\nTOKEN_PATH=" + shellQuote(tokenPath) + "\n" +
		`set -eu
# Never stop an already running service on a failed retry: the installer
# refuses active services before writing credentials.
sudo_cmd rm -f /var/lib/runnerhub-android/state/enrollment-token
rm -f "$TOKEN_PATH"`
}
func diagnosticsAndroidNativeScript(req Request) string {
	req.RegistryUsername = ""
	req.RegistryPassword = ""
	return commonScript(req) + "\n" +
		`set -eu
if command -v systemctl >/dev/null 2>&1; then
  sudo_cmd systemctl --no-pager status runnerhub-android-agent 2>&1 || true
  sudo_cmd journalctl -u runnerhub-android-agent -n 30 --no-pager 2>&1 || true
fi`
}
