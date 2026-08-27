#!/usr/bin/env bash
# Normalize one Codex execution into cictl's reviewer result envelope.
set -Eeuo pipefail

prompt_file="${1:?prompt file required}"
stream_file="${2:?stream file required}"
err_file="${3:?stderr file required}"
model="${REVIEW_MODEL:-default}"

[[ "${CODEX_HOME:-}" == /* ]] || {
  printf 'run-codex: CODEX_HOME must be an absolute path\n' >&2
  exit 1
}
codex login status >/dev/null

last_message="$(mktemp)"
trap 'rm -f "$last_message"' EXIT

model_args=()
if [[ "$model" != "default" ]]; then
  model_args=(--model "$model")
fi

set +e
env -i \
  PATH="$PATH" \
  HOME="${HOME:-/tmp}" \
  CODEX_HOME="$CODEX_HOME" \
  codex \
  --ask-for-approval never \
  --sandbox read-only \
  --disable shell_tool \
  --disable unified_exec \
  --disable image_generation \
  --disable browser_use \
  --disable apps \
  --disable multi_agent \
  exec \
  --ephemeral \
  --ignore-user-config \
  --strict-config \
  --cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" \
  --skip-git-repo-check \
  --json \
  --output-last-message "$last_message" \
  ${model_args[@]+"${model_args[@]}"} \
  - < "$prompt_file" > "$stream_file" 2> "$err_file"
rc=$?
set -e

if [[ $rc -eq 0 && -s "$last_message" ]]; then
  jq -n --rawfile result "$last_message" \
    '{type:"result",subtype:"success",is_error:false,result:$result,terminal_reason:"completed",provider:"codex"}' \
    >> "$stream_file"
fi

exit "$rc"
