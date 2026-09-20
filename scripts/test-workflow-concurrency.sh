#!/usr/bin/env bash
set -euo pipefail

workflow_dir=".github/workflows"

require_fixed() {
  local file="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$file"; then
    echo "expected '$needle' in $file" >&2
    exit 1
  fi
}

ordinary_workflows=(
  "$workflow_dir/ci.yml"
  "$workflow_dir/deployment-ci.yml"
  "$workflow_dir/dind-runtime-ci.yml"
  "$workflow_dir/hardening-validation.yml"
  "$workflow_dir/secret-init-ci.yml"
  "$workflow_dir/truenas-compose-prep-ci.yml"
)

for workflow in "${ordinary_workflows[@]}"; do
  require_fixed "$workflow" "cancel-in-progress: true"
done

# PR/push CI must scope supersession to the same branch or pull request so
# unrelated work can still run concurrently.
scoped_workflows=(
  "$workflow_dir/ci.yml"
  "$workflow_dir/deployment-ci.yml"
  "$workflow_dir/dind-runtime-ci.yml"
  "$workflow_dir/secret-init-ci.yml"
  "$workflow_dir/truenas-compose-prep-ci.yml"
)

for workflow in "${scoped_workflows[@]}"; do
  require_fixed "$workflow" '${{ github.event.pull_request.number || github.ref }}'
done

# Publishing is stateful: newer publishes for the same ref must queue behind an
# in-progress publish instead of interrupting it halfway through.
require_fixed "$workflow_dir/publish-images.yml" "cancel-in-progress: false"

echo "GitHub Actions concurrency policy is valid."
