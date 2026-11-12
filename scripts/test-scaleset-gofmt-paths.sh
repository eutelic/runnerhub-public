#!/usr/bin/env bash
set -euo pipefail

# Regression for the scale-set formatting lane: git emits root-relative paths.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git -C "$tmp" init -q
mkdir -p "$tmp/scaleset/nested/deeper" "$tmp/scaleset/with spaces" "$tmp/internal"
printf 'package main\n' > "$tmp/scaleset/root.go"
printf 'package nested\n' > "$tmp/scaleset/nested/deep.go"
printf 'package deeper\n' > "$tmp/scaleset/nested/deeper/deepest.go"
printf 'package spaced\n' > "$tmp/scaleset/with spaces/spaced file.go"
printf 'package internal\n' > "$tmp/internal/unrelated.go"
printf 'package removed\n' > "$tmp/scaleset/deleted.go"
git -C "$tmp" add .
rm "$tmp/scaleset/deleted.go"
# An untracked source must not affect a tracked-file-only formatting check.
printf 'package main\nfunc  untracked( ) {}\n' > "$tmp/scaleset/untracked.go"
cd "$tmp"
mapfile -d '' tracked < <(git ls-files -z -- 'scaleset/*.go' 'scaleset/**/*.go')
# Deleted tracked files remain in the index, but gofmt must not receive them.
[[ " ${tracked[*]} " == *" scaleset/deleted.go "* ]] || {
  echo 'Deleted-file fixture is not tracked' >&2
  exit 1
}
files=()
for file in "${tracked[@]}"; do
  [[ -f "$file" ]] && files+=("$file")
done
if [[ "${#files[@]}" -ne 4 ]]; then
  printf 'Expected four scale-set files, got %s\n' "${#files[@]}" >&2
  exit 1
fi
for file in "${files[@]}"; do
  [[ "$file" == scaleset/* && -f "$file" ]] || {
    printf 'Unexpected or unresolved path: %s\n' "$file" >&2
    exit 1
  }
done
[[ -z "$(gofmt -l "${files[@]}")" ]] || {
  echo 'Fixture Go sources should be formatted' >&2
  exit 1
}

# A formatting defect in another module must not fail the scale-set lane.
printf 'package internal\nfunc  broken( ) {}\n' > internal/unrelated.go
[[ -z "$(gofmt -l "${files[@]}")" ]] || {
  echo 'Unrelated module unexpectedly affected scale-set formatting' >&2
  exit 1
}

# Both direct and nested scale-set sources must be checked, not merely selected.
for target in scaleset/root.go scaleset/nested/deep.go scaleset/nested/deeper/deepest.go "scaleset/with spaces/spaced file.go"; do
  printf 'package main\nfunc  broken( ) {}\n' > "$target"
  if ! gofmt -l "${files[@]}" | grep -Fxq "$target"; then
    printf 'Formatting defect was not detected in %s\n' "$target" >&2
    exit 1
  fi
  gofmt -w "$target"
done
[[ -z "$(gofmt -l "${files[@]}")" ]] || {
  echo 'Formatting defects remained after repair' >&2
  exit 1
}

# The root-module formatting lane also sees index entries for deleted files.
# Verify it filters missing paths while preserving unrelated tracked Go files.
mapfile -d '' root_tracked < <(git ls-files -z -- '*.go' ':(exclude)scaleset/**')
root_files=()
for file in "${root_tracked[@]}"; do
  [[ -f "$file" ]] && root_files+=("$file")
done
[[ "${#root_files[@]}" -eq 1 ]] || {
  printf 'Expected one existing tracked Go file in root lane, got %s\n' "${#root_files[@]}" >&2
  exit 1
}
[[ " ${root_files[*]} " == *" internal/unrelated.go "* ]] || {
  echo 'Root lane lost an unrelated tracked Go file' >&2
  exit 1
}
[[ " ${root_files[*]} " != *" scaleset/deleted.go "* ]] || {
  echo 'Root lane included a deleted tracked Go file' >&2
  exit 1
}

[[ " ${root_files[*]} " != *" scaleset/root.go "* ]] || {
  echo 'Root formatting lane included scale-set module sources' >&2
  exit 1
}

# Verify the root lane actually detects a formatting defect in its own module,
# without accidentally pulling in nested scale-set files or deleted entries.
[[ "$(gofmt -l "${root_files[@]}")" == "internal/unrelated.go" ]] || {
  echo 'Root lane failed to detect its tracked formatting defect' >&2
  exit 1
}
gofmt -w internal/unrelated.go
[[ -z "$(gofmt -l "${root_files[@]}")" ]] || {
  echo 'Root formatting defect remained after repair' >&2
  exit 1
}
