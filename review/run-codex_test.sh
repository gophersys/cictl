#!/usr/bin/env bash
set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { printf 'run-codex-test: FAIL: %s\n' "$*" >&2; exit 1; }

mkdir -p "$WORK/bin" "$WORK/home"
printf '{}\n' > "$WORK/home/auth.json"
printf 'review this\n' > "$WORK/prompt"

cat > "$WORK/bin/codex" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CODEX_TEST_CALLS"
if [[ "$*" == *"login status"* ]]; then
  exit 0
fi
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
CODEX_TEST_CALLS="$WORK/calls" \
REVIEW_MODEL=default \
bash "$HERE/run-codex.sh" "$WORK/prompt" "$WORK/stream" "$WORK/error"

first="$(sed -n '1p' "$WORK/calls")"
second="$(sed -n '2p' "$WORK/calls")"
[[ "$first" == "login status" ]] || fail "authentication was not checked first: $first"
[[ "$second" == *"--ask-for-approval never --sandbox read-only exec"* ]] || fail "approval/sandbox were not fixed before exec: $second"
[[ "$second" == *"--ephemeral"* && "$second" == *"--ignore-user-config"* && "$second" == *"--strict-config"* ]] || fail "automation flags are incomplete: $second"
jq -e -s 'any(.[]; .type == "turn.completed") and any(.[]; .type == "result" and .provider == "codex" and .result == "APPROVE\n")' "$WORK/stream" >/dev/null \
  || fail "stream was not preserved and normalized"

printf 'run-codex-test: OK\n'
