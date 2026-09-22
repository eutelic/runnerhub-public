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

# Branch snapshot publication is supersedable, but immutable tag publication
# must finish. Keep that distinction explicit in the workflow policy.
require_fixed "$workflow_dir/publish-images.yml" "cancel-in-progress:"
require_fixed "$workflow_dir/publish-images.yml" "github.ref_type == 'branch'"

# Merged PRs use a different concurrency group from their resulting main push.
# The cleanup workflow must cancel only ordinary PR validation and must never
# cancel the stateful publish-images workflow.
cleanup="$workflow_dir/cleanup-merged-pr-runs.yml"
require_fixed "$cleanup" "pull_request:"
require_fixed "$cleanup" "types: [closed]"
require_fixed "$cleanup" "github.event.pull_request.merged == true"
require_fixed "$cleanup" "actions: write"
require_fixed "$cleanup" "runs-on: runnerhub"
require_fixed "$cleanup" "event=pull_request&head_sha="
require_fixed "$cleanup" "/cancel"
if grep -Fq -- "publish-images)" "$cleanup"; then
  echo "cleanup workflow must not cancel publish-images" >&2
  exit 1
fi

echo "GitHub Actions concurrency policy is valid."
