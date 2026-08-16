#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
scanner_image="ghcr.io/gitleaks/gitleaks:v8.30.1@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f"
scan_root="$(mktemp -d "${TMPDIR:-/tmp}/hookfly-secret-scan.XXXXXX")"

cleanup() {
  rm -rf -- "$scan_root"
}
trap cleanup EXIT

mkdir "$scan_root/tree"
tree_id="$(git -C "$repo_root" write-tree)"
git -C "$repo_root" archive "$tree_id" | tar -xf - -C "$scan_root/tree"

run_gitleaks() {
  local source=$1
  shift
  docker run --rm \
    --network none \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
    --user "$(id -u):$(id -g)" \
    --volume "$source:/repo:ro" \
    --workdir /repo \
    "$scanner_image" \
    detect \
    --source=/repo \
    --redact=100 \
    --no-banner \
    --no-color \
    --exit-code=1 \
    "$@"
}

run_gitleaks "$scan_root/tree" --no-git
run_gitleaks "$repo_root" --log-opts=--all
