#!/usr/bin/env bash
# Run as the actual dedicated RunnerHub process-provider account, not root.
set -euo pipefail
root="${1:-$HOME}"
fail=0
check() {
  if "$@"; then printf '[ ok ] %s\n' "$*"; else
    printf '[fail] %s\n' "$*" >&2; fail=1
  fi
}
linux_amd64() { [[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]; }
kvm_ioctl() {
  python3 - <<'PY'
import fcntl, os, stat, sys
path = "/dev/kvm"
try:
    st = os.stat(path)
    if not stat.S_ISCHR(st.st_mode):
        raise OSError("not a character device")
    fd = os.open(path, os.O_RDWR | os.O_CLOEXEC)
    try:
        if fcntl.ioctl(fd, 0xAE00, 0) != 12:
            raise OSError("KVM API is not version 12")
    finally:
        os.close(fd)
except (OSError, PermissionError) as e:
    print(f"KVM unavailable for current user: {e}", file=sys.stderr)
    sys.exit(1)
PY
}
cpu() { [[ "$(getconf _NPROCESSORS_ONLN)" -ge 4 ]]; }
mem() { awk '/^MemAvailable:/ { found=1; if ($2 >= 8388608) exit 0; else exit 1 } END { if (!found) exit 1 }' /proc/meminfo; }
disk() {
  local free
  free="$(df -Pk "$root" | awk 'END {print $4}')"
  [[ "$free" =~ ^[0-9]+$ && "$free" -ge 20971520 ]]
}
echo "Checking native Android emulator host as $(id -un) on $(hostname)"
check linux_amd64
check kvm_ioctl
check cpu
check mem
check disk
if [[ "$fail" != 0 ]]; then
  echo "Host is NOT ready. Do not enable runnerhub-android-emulator." >&2
  exit 1
fi
echo "Host hardware prerequisites passed. This is not a successful emulator gameplay test."
