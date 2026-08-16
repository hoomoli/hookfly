#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/hookfly-policy.XXXXXX")
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM

reject_path() {
  case_name=$1
  candidate_path=$2
  expected_message=$3
  case_root="$fixture_root/$case_name"

  mkdir -p "$case_root/.github/workflows" "$case_root/deploy" "$case_root/scripts" "$case_root/web"
  mkdir -p "$(dirname -- "$case_root/$candidate_path")"
  cp "$repo_root/scripts/check-repository.sh" "$case_root/scripts/check-repository.sh"
  chmod +x "$case_root/scripts/check-repository.sh"
  printf '%s\n' 'FROM scratch' > "$case_root/Dockerfile.backend"
  printf '%s\n' 'FROM scratch' > "$case_root/web/Dockerfile"
  : > "$case_root/.github/workflows/ci.yml"
  : > "$case_root/README.md"
  : > "$case_root/deploy/compose.example.yaml"
  : > "$case_root/.env.example"
  printf '%s\n' 'local-only fixture' > "$case_root/$candidate_path"

  git -C "$case_root" init -q
  git -C "$case_root" add .
  if output=$(cd "$case_root" && scripts/check-repository.sh 2>&1); then
    printf 'policy accepted forbidden path: %s\n' "$candidate_path" >&2
    exit 1
  fi
  case "$output" in
    *"$expected_message"*) ;;
    *)
      printf 'unexpected policy error for %s: %s\n' "$candidate_path" "$output" >&2
      exit 1
      ;;
  esac
}

reject_path root-agent AGENTS.md 'Agent/AI or maintenance material'
reject_path agent-rule .agents/rules/open-source-commit.md 'Agent/AI or maintenance material'
reject_path agent-plan .agent-work/plan.md 'Agent/AI or maintenance material'
reject_path handoff HANDOFF.md 'Agent/AI or maintenance material'
reject_path certificate server.crt 'Sensitive runtime filenames'
reject_path backup release-backup.zip 'Sensitive runtime filenames'
reject_path browser-output web/blob-report/report.json 'Generated output'

printf '%s\n' 'Repository policy self-tests passed.'
