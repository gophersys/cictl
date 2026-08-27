#!/usr/bin/env bash
set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
REAL_CODEX="$(command -v codex)" || {
  printf 'run-codex-test: FAIL: codex is not installed\n' >&2
  exit 127
}

fail() { printf 'run-codex-test: FAIL: %s\n' "$*" >&2; exit 1; }

mkdir -p "$WORK/bin" "$WORK/home"
printf '{}\n' > "$WORK/home/auth.json"
printf 'review this\n' > "$WORK/prompt"

cat > "$WORK/bin/codex" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CODEX_HOME/../calls"
if [[ "$*" == *"login status"* ]]; then
  exit 0
fi
printf 'GH_TOKEN=%s OPENAI_API_KEY=%s\n' "${GH_TOKEN-unset}" "${OPENAI_API_KEY-unset}" > "$CODEX_HOME/../env"
cat >/dev/null
printf '%s\n' \
  '{"type":"thread.started","thread_id":"t-1"}' \
  '{"type":"turn.started"}' \
  '{"type":"item.completed","item":{"id":"i-1","type":"agent_message","text":"APPROVE"}}' \
  '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":1}}'
for arg in "$@"; do
  if [[ "$arg" == "--output-last-message" ]]; then
    write_next=yes
  elif [[ "${write_next:-no}" == yes ]]; then
    printf 'APPROVE\n' > "$arg"
    write_next=no
  fi
done
EOF
chmod +x "$WORK/bin/codex"

PATH="$WORK/bin:$PATH" \
CODEX_HOME="$WORK/home" \
GH_TOKEN=must-not-reach-model \
OPENAI_API_KEY=must-not-reach-model \
REVIEW_MODEL=default \
bash "$HERE/run-codex.sh" "$WORK/prompt" "$WORK/stream" "$WORK/error"

first="$(sed -n '1p' "$WORK/calls")"
second="$(sed -n '2p' "$WORK/calls")"
[[ "$first" == "login status" ]] || fail "authentication was not checked first: $first"
[[ "$second" == *"--ask-for-approval never --sandbox read-only"* ]] || fail "approval/sandbox boundary is incomplete: $second"
[[ "$second" == *"--config tools.view_image=false"* ]] || fail "view_image is not disabled: $second"
[[ "$second" == *"--ephemeral"* && "$second" == *"--ignore-user-config"* ]] || fail "automation flags are incomplete: $second"
[[ "$second" != *"--strict-config"* ]] || fail "deployed Codex rejects the legacy image-disable key in strict mode: $second"
[[ "$second" == *"--skip-git-repo-check"* ]] || fail "Codex was not isolated from pull-request instructions: $second"
[[ "$second" != *"--model default"* ]] || fail "the neutral model sentinel reached Codex: $second"
grep -qx 'GH_TOKEN=unset OPENAI_API_KEY=unset' "$WORK/env" || fail "a CI credential reached the model process"
jq -e -s 'any(.[]; .type == "turn.completed") and any(.[]; .type == "result" and .provider == "codex" and .result == "APPROVE\n")' "$WORK/stream" >/dev/null \
  || fail "stream was not preserved and normalized"

codex_help="$("$REAL_CODEX" --help)"
exec_help="$("$REAL_CODEX" exec --help)"
features="$("$REAL_CODEX" features list)"
grep -q -- '--ask-for-approval' <<< "$codex_help" || fail "installed Codex has no approval-policy flag"
grep -q -- '--ignore-user-config' <<< "$exec_help" || fail "installed Codex has no isolated-config flag"
expected_disabled_features="$(printf '%s\n' shell_tool unified_exec image_generation browser_use apps multi_agent)"
disabled_features="$(sed -n 's/^[[:space:]]*--disable \([^[:space:]\\]*\).*/\1/p' "$HERE/run-codex.sh")"
[[ "$disabled_features" == "$expected_disabled_features" ]] || fail "Codex deny-list drifted: expected [$expected_disabled_features], got [$disabled_features]"
while IFS= read -r feature; do
  [[ -n "$feature" ]] || continue
  grep -q "^${feature}[[:space:]]" <<< "$features" || fail "installed Codex has no feature named $feature"
done <<< "$disabled_features"

mkdir -p "$WORK/noauth"
set +e
config_probe="$(CODEX_HOME="$WORK/noauth" "$REAL_CODEX" --config tools.view_image=false exec --skip-git-repo-check --json - </dev/null 2>&1)"
config_rc=$?
set -e
[[ $config_rc -ne 0 && "$config_probe" == *"No prompt provided via stdin."* ]] \
  || fail "installed Codex did not accept the legacy image-disable key without inference: $config_probe"

printf 'run-codex-test: OK\n'
