#!/bin/sh
set -eu

fail() {
  printf '%b\n' "$1" >&2
  exit 1
}

non_ascii_paths=$(git ls-files | LC_ALL=C grep '[^ -~]' || true)
[ -z "$non_ascii_paths" ] || fail "Tracked paths must use ASCII characters:\n$non_ascii_paths"

agent_materials=$(git ls-files | grep -Ei '(^|/)(AGENTS?\.md|CLAUDE\.md|GEMINI\.md|HANDOFF\.md|\.agents|\.agent-work|\.logic-lines|\.superpowers|\.codex|docs/superpowers)(/|$)' || true)
[ -z "$agent_materials" ] || fail "Agent/AI or maintenance material must stay local and ignored:\n$agent_materials"

chinese=$(git grep -I -n -P '[\x{4e00}-\x{9fff}]' -- . \
  ':(exclude)web/src/locales/zh-CN.ts' \
  ':(exclude)web/src/components/LanguageMenu.test.tsx' \
  ':(exclude)web/src/App.test.tsx' \
  ':(exclude)web/e2e/apple-ui.spec.ts' || true)
[ -z "$chinese" ] || fail "Chinese text is outside the approved localization files:\n$chinese"

forbidden_names=$(git ls-files | grep -E '(^|/)(\.env($|\.)|\.npmrc$|\.netrc$|services\.json$|config\.ya?ml$|.*\.(db|db-shm|db-wal|sqlite|sqlite3|log|key|pem|p12|pfx|crt|cer|der|jks|keystore|credentials|secret|bak|backup|old|orig|save|zip|tar|tgz|7z)$)' \
  | grep -v -E '^(\.env\.example|\.github/ISSUE_TEMPLATE/config\.yml)$' || true)
[ -z "$forbidden_names" ] || fail "Sensitive runtime filenames are tracked:\n$forbidden_names"

generated=$(git ls-files | grep -E '(^|/)(node_modules|dist|bin|obj|coverage|test-results|playwright-report|blob-report|screenshots?|artifacts?)(/|$)|(^|/)(coverage\.out|.*\.tsbuildinfo)$' || true)
[ -z "$generated" ] || fail "Generated output is tracked:\n$generated"

floating_actions=$(grep -nE '^[[:space:]]*- uses:' .github/workflows/*.yml | grep -Ev '@[0-9a-f]{40}([[:space:]]+#.*)?$' || true)
[ -z "$floating_actions" ] || fail "GitHub Actions must use full commit SHAs:\n$floating_actions"

unpinned_images=$(awk '$1 == "FROM" && $2 != "scratch" && $2 !~ /@sha256:[0-9a-f]{64}$/ { print FILENAME ":" FNR ":" $0 }' Dockerfile.backend web/Dockerfile)
[ -z "$unpinned_images" ] || fail "Docker base images must use sha256 digests:\n$unpinned_images"

if grep -Eq 'Preact|HOOKFLY_WIREGUARD_IP' README.md deploy/compose.example.yaml .env.example; then
  fail "Public documentation contains stale implementation or binding names."
fi

printf '%s\n' "Repository policy checks passed."
