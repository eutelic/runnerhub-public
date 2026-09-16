#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "RunnerHub JIT acceptance failed: $*" >&2
  exit 1
}

: "${RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER:?required}"
: "${RUNNERHUB_ACCEPTANCE_GITHUB_APP_PRIVATE_KEY_FILE:?required}"
: "${RUNNERHUB_ACCEPTANCE_REPOSITORY:?required}"
: "${RUNNERHUB_ACCEPTANCE_RUNNER_IMAGE:?required}"
: "${RUNNERHUB_ACCEPTANCE_GITHUB_TOKEN:?required}"
: "${GITHUB_RUN_ID:?required}"

[[ "$RUNNERHUB_ACCEPTANCE_REPOSITORY" == */* ]] || fail "RUNNERHUB_ACCEPTANCE_REPOSITORY must be owner/repository"
owner="${RUNNERHUB_ACCEPTANCE_REPOSITORY%%/*}"
repository="${RUNNERHUB_ACCEPTANCE_REPOSITORY#*/}"
[ -n "$owner" ] && [ -n "$repository" ] || fail "invalid repository"
[ -s "$RUNNERHUB_ACCEPTANCE_GITHUB_APP_PRIVATE_KEY_FILE" ] || fail "GitHub App private key file is empty"

[[ "$RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER" =~ ^[0-9]+$ ]] || fail "RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER must contain the numeric GitHub App ID"
github_api="${RUNNERHUB_ACCEPTANCE_GITHUB_API_URL:-https://api.github.com}"

b64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

jwt_header="$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | b64url)"
jwt_payload="$(APP_ID="$RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER" python3 - <<'PY' | b64url
import json, os, time
now=int(time.time())
print(json.dumps({"iat":now-60,"exp":now+540,"iss":int(os.environ["APP_ID"])}, separators=(",",":")))
PY
)"
jwt_unsigned="$jwt_header.$jwt_payload"
jwt_signature="$(printf '%s' "$jwt_unsigned" | openssl dgst -sha256 -sign "$RUNNERHUB_ACCEPTANCE_GITHUB_APP_PRIVATE_KEY_FILE" -binary | b64url)"
bootstrap_jwt="$jwt_unsigned.$jwt_signature"

app_metadata="$(curl --fail-with-body --silent --show-error \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer $bootstrap_jwt" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "$github_api/app")" || fail "could not read GitHub App metadata with numeric App ID"

app_client_id="$(printf '%s' "$app_metadata" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("client_id") or "")')"
app_slug="$(printf '%s' "$app_metadata" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("slug") or "")')"
app_numeric_id="$(printf '%s' "$app_metadata" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id") or "")')"
[ "$app_numeric_id" = "$RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER" ] || fail "GitHub App metadata ID does not match configured acceptance App ID"
[ -n "$app_client_id" ] || fail "GitHub App metadata did not include client_id"
[ -n "$app_slug" ] || fail "GitHub App metadata did not include slug"

echo "Live acceptance verified GitHub App metadata for $app_slug and will use explicit App ID + Client ID production configuration."
root="$(mktemp -d)"
run_id="${GITHUB_RUN_ID:-local-$$}"
pool_id="release-acceptance-${run_id}"
runner_label="runnerhub-release-acceptance"
admin_key="$(openssl rand -hex 32)"
enrollment_token="$(openssl rand -hex 32)"
agent_secret="$(openssl rand -hex 32)"
control_log="$root/control.log"
agent_log="$root/agent.log"

port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
control_url="http://127.0.0.1:$port"
actions_jobs_url="https://api.github.com/repos/$RUNNERHUB_ACCEPTANCE_REPOSITORY/actions/runs/$GITHUB_RUN_ID/jobs?per_page=100"

acceptance_job_json() {
  curl --fail --silent --show-error     -H "Accept: application/vnd.github+json"     -H "Authorization: Bearer $RUNNERHUB_ACCEPTANCE_GITHUB_TOKEN"     -H "X-GitHub-Api-Version: 2022-11-28"     "$actions_jobs_url" | python3 -c '
import json, sys
data=json.load(sys.stdin)
for job in data.get("jobs", []):
    if job.get("name") == "Execute real JIT acceptance job":
        print(json.dumps(job))
        break
'
}

control_pid=""
agent_pid=""
cleanup() {
  set +e
  [ -n "$agent_pid" ] && kill "$agent_pid" >/dev/null 2>&1
  [ -n "$control_pid" ] && kill "$control_pid" >/dev/null 2>&1
  docker ps -aq --filter "label=io.runnerhub.pool=$pool_id" | xargs -r docker rm -f >/dev/null 2>&1
  rm -rf "$root"
}
trap cleanup EXIT

dump_failure_context() {
  echo "--- RunnerHub worker state ---" >&2
  curl --silent --show-error     -H "Authorization: Bearer $admin_key"     "$control_url/api/v1/workers" 2>/dev/null | python3 -m json.tool >&2 || true
  echo "--- control tail ---" >&2
  tail -n 120 "$control_log" >&2 2>/dev/null || true
  echo "--- agent tail ---" >&2
  tail -n 120 "$agent_log" >&2 2>/dev/null || true
}

echo "Building RunnerHub acceptance binaries (no container images)..."
go build -o "$root/runnerhubd" ./cmd/runnerhubd
go build -o "$root/runnerhub-agent" ./cmd/runnerhub-agent

RUNNERHUB_DB="$root/runnerhub.db" RUNNERHUB_HTTP_ADDR="127.0.0.1:$port" RUNNERHUB_ENROLLMENT_TOKEN="$enrollment_token" RUNNERHUB_AGENT_SECRET="$agent_secret" RUNNERHUB_API_KEYS="$admin_key:admin:release-acceptance" RUNNERHUB_GITHUB_APP_ID="$RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER" RUNNERHUB_GITHUB_APP_CLIENT_ID="$app_client_id" RUNNERHUB_GITHUB_APP_SLUG="$app_slug" RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE="$RUNNERHUB_ACCEPTANCE_GITHUB_APP_PRIVATE_KEY_FILE" RUNNERHUB_ALLOWED_IMAGES="$RUNNERHUB_ACCEPTANCE_RUNNER_IMAGE" RUNNERHUB_REQUIRE_PINNED_IMAGES=true "$root/runnerhubd" >"$control_log" 2>&1 &
control_pid=$!

ready=false
for _ in $(seq 1 60); do
  if curl --fail --silent "$control_url/healthz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  kill -0 "$control_pid" >/dev/null 2>&1 || {
    dump_failure_context
    fail "control plane exited before becoming ready"
  }
  sleep 1
done
[ "$ready" = true ] || {
  dump_failure_context
  fail "control plane did not become ready"
}

RUNNERHUB_CONTROL_PLANE_URL="$control_url" RUNNERHUB_ALLOW_INSECURE_HTTP=true RUNNERHUB_ENROLLMENT_TOKEN="$enrollment_token" RUNNERHUB_PROVIDER=docker RUNNERHUB_STATE_DIR="$root/agent-state" RUNNERHUB_CREDENTIAL_FILE="$root/agent-credentials.json" RUNNERHUB_HOST_NAME="release-acceptance-host" RUNNERHUB_RUNNER_IMAGE="$RUNNERHUB_ACCEPTANCE_RUNNER_IMAGE" RUNNERHUB_DOCKER_PULL_POLICY=always "$root/runnerhub-agent" >"$agent_log" 2>&1 &
agent_pid=$!

host_ready=false
for _ in $(seq 1 60); do
  hosts="$(curl --fail --silent     -H "Authorization: Bearer $admin_key"     "$control_url/api/v1/hosts" 2>/dev/null || true)"
  if printf '%s' "$hosts" | python3 -c '
import json, sys
try:
    hosts=json.load(sys.stdin)
except Exception:
    raise SystemExit(1)
raise SystemExit(0 if any(h.get("provider")=="docker" and h.get("state")=="online" for h in hosts) else 1)
' >/dev/null 2>&1; then
    host_ready=true
    break
  fi
  kill -0 "$agent_pid" >/dev/null 2>&1 || {
    dump_failure_context
    fail "Docker agent exited before enrollment"
  }
  sleep 1
done
[ "$host_ready" = true ] || {
  dump_failure_context
  fail "Docker agent did not enroll"
}

pool_payload="$(python3 - <<PY
import json
print(json.dumps({
  "id": "$pool_id",
  "name": "release-acceptance",
  "scope": {"kind": "repository", "owner": "$owner", "repository": "$repository"},
  "labels": ["$runner_label"],
  "isolation": "container-trusted",
  "resources": {"cpuMillis": 1000, "memoryBytes": 2147483648, "diskBytes": 4294967296},
  "minIdle": 0,
  "maxConcurrency": 1,
  "runnerGroupId": 1,
  "image": "$RUNNERHUB_ACCEPTANCE_RUNNER_IMAGE"
}))
PY
)"

pool_create_response="$root/pool-create-response.json"
if ! curl --fail-with-body --silent --show-error   -X POST   -H "Authorization: Bearer $admin_key"   -H "Content-Type: application/json"   "$control_url/api/v1/pools"   -d "$pool_payload" >"$pool_create_response"; then
    echo "--- pool creation response ---" >&2
    cat "$pool_create_response" >&2 || true
    dump_failure_context
    fail "could not create acceptance pool"
fi

queued_seen=false
for _ in $(seq 1 30); do
  job="$(acceptance_job_json || true)"
  if [ -n "$job" ]; then
    job_status="$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')"
    if [ "$job_status" = "queued" ]; then
      queued_seen=true
      break
    fi
  fi
  sleep 1
done
[ "$queued_seen" = true ] || {
  dump_failure_context
  fail "acceptance job was not observed queued before runner provisioning"
}

worker_response="$(curl --fail-with-body --silent --show-error   -X POST   -H "Authorization: Bearer $admin_key"   -H "Content-Type: application/json"   "$control_url/api/v1/pools/$pool_id/workers"   -d "$(python3 - <<PY
import json
print(json.dumps({
  "repository": "$RUNNERHUB_ACCEPTANCE_REPOSITORY",
  "workflow": ".github/workflows/publish-images.yml",
  "public": False
}))
PY
)")" || {
    dump_failure_context
    fail "could not request real JIT worker"
  }

worker_id="$(printf '%s' "$worker_response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
runner_name="$(printf '%s' "$worker_response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["runnerName"])')"
[ -n "$worker_id" ] || fail "worker response did not contain an id"

echo "Waiting for real JIT worker $worker_id to register, run the acceptance job, and clean up..."
deadline=$((SECONDS + 540))
last_state=""
assigned_seen=false
while (( SECONDS < deadline )); do
  worker="$(curl --fail --silent     -H "Authorization: Bearer $admin_key"     "$control_url/api/v1/workers" | WORKER_ID="$worker_id" python3 -c '
import json, os, sys
workers=json.load(sys.stdin)
wid=os.environ["WORKER_ID"]
for w in workers:
    if w.get("id")==wid:
        print(json.dumps(w))
        break
')"
  [ -n "$worker" ] || {
    dump_failure_context
    fail "acceptance worker disappeared from control-plane state"
  }

  state="$(printf '%s' "$worker" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("state",""))')"
  failure="$(printf '%s' "$worker" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("failureReason",""))')"
  if [ "$state" != "$last_state" ]; then
    echo "worker state: $state"
    last_state="$state"
  fi
  if [ -n "$failure" ]; then
    printf '%s\n' "$worker" | python3 -m json.tool >&2 || true
    dump_failure_context
    fail "worker failed: $failure"
  fi

  job="$(acceptance_job_json || true)"
  if [ -n "$job" ]; then
    job_status="$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')"
    job_runner="$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("runner_name") or "")')"
    if [ "$job_status" = "in_progress" ] && [ "$job_runner" = "$runner_name" ]; then
      assigned_seen=true
    fi
  fi

  if [ "$state" = "deleted" ]; then
    printf '%s' "$worker" | EXPECTED_IMAGE="$RUNNERHUB_ACCEPTANCE_RUNNER_IMAGE" python3 -c '
import json, os, sys
w=json.load(sys.stdin)
required=["registeredAt","startedAt","completedAt"]
missing=[key for key in required if not w.get(key)]
if missing:
    raise SystemExit("missing lifecycle evidence: "+", ".join(missing))
if w.get("image") != os.environ["EXPECTED_IMAGE"]:
    raise SystemExit("worker did not use the exact pinned runner image")
if w.get("failureReason"):
    raise SystemExit("worker retained a failure reason: "+w["failureReason"])
'
    [ "$assigned_seen" = true ] || fail "GitHub job was never observed assigned/in progress on $runner_name"
    completed_seen=false
    for _ in $(seq 1 30); do
      job="$(acceptance_job_json || true)"
      if [ -n "$job" ]; then
        job_status="$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')"
        job_conclusion="$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("conclusion") or "")')"
        if [ "$job_status" = "completed" ] && [ "$job_conclusion" = "success" ]; then
          completed_seen=true
          break
        fi
      fi
      sleep 1
    done
    [ "$completed_seen" = true ] || fail "GitHub acceptance job did not reach completed/success"
    echo "Real GitHub JIT acceptance passed: queued -> assigned -> started -> completed -> deleted."
    exit 0
  fi
  sleep 2
done

dump_failure_context
fail "timed out waiting for JIT worker lifecycle"
