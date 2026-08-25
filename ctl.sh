#!/usr/bin/env bash
#
# tools/cictl/ctl.sh — control script for cictl, the Eden CI contract tool
# (go-first-emit). cictl emits the CI contract's JSON Schema from Go structs,
# validates an instance, renders the provider workflows deterministically,
# drift-checks them (the CI-on-CI gate), asserts a repo's .ci/ is canonical,
# lists affected project roots, and reports the pinned-version matrix.
#
# Usage: ./ctl.sh <command> [args...]
#
# Verbs are uniform with the monorepo's nx:run-commands convention: project.json
# targets are thin wrappers that delegate here, so a consumer can invoke
# `nx run cictl:test` without reading source.
#
set -Eeuo pipefail
IFS=$'\n\t'

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GOLANGCI_CONFIG="${PROJECT_ROOT}/.golangci.yml"   # vendored at extraction; see the header in that file

# -------- logging --------
function log_info()    { printf '\033[0;36m[info]\033[0m  %s\n' "$*"; }
function log_warn()    { printf '\033[0;33m[warn]\033[0m  %s\n' "$*" >&2; }
function log_error()   { printf '\033[0;31m[error]\033[0m %s\n' "$*" >&2; }
function log_success() { printf '\033[0;32m[ok]\033[0m    %s\n' "$*"; }

# -------- tool gate --------
function require_cmd() {
  local missing=()
  local cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    log_error "missing required tool(s): ${missing[*]}"
    exit 127
  fi
}

# -------- commands --------
function cmd_build() {
  require_cmd go
  log_info "build: go build ./..."
  # GOWORK=off isolates the tool from the dev workspace (10 §6.1, ADR-0018).
  (cd "$PROJECT_ROOT" && GOWORK=off go build ./...)
  log_success "build: OK"
}

function cmd_test() {
  require_cmd go
  log_info "test: go test -race ./..."
  (cd "$PROJECT_ROOT" && GOWORK=off go test -race ./...)
  cmd_test_review
  cmd_test_action
  log_success "test: OK"
}

# cmd_test_review — the guard suite for review/review.sh, the script that spawns
# a paid agent and writes to pull requests. It runs against stub `claude` and
# `gh` commands on a sandbox PATH, so it costs nothing and reaches no network.
# Its second phase removes each guard in turn and requires the matching test to
# fail, because a guard that no test can break is a guard nobody has checked.
function cmd_test_review() {
  require_cmd bash sed cmp env ln cp
  log_info "test-review: review/review_test.sh (guards, then mutation proof)"
  (cd "$PROJECT_ROOT" && bash review/review_test.sh)
  log_success "test-review: OK"
}

# cmd_test_action — the same 3 phases for review/action.yml, the COMPOSITE ACTION
# every other repository calls. Its assertions read the action, the scripts it
# dispatches to, the repository those scripts point into, and the caller the real
# generator emits; each one is then re-run against a copy with 1 counter-stimulus
# applied and must fail.
function cmd_test_action() {
  require_cmd bash actionlint go awk diff find sort
  log_info "test-action: review/action_test.sh (the one home, then discrimination)"
  (cd "$PROJECT_ROOT" && bash review/action_test.sh)
  log_success "test-action: OK"
}

function cmd_fmt() {
  require_cmd gofumpt
  log_info "fmt: gofumpt -w ."
  (cd "$PROJECT_ROOT" && gofumpt -w .)
  log_success "fmt: OK"
}

function cmd_vet() {
  require_cmd go
  log_info "vet: go vet ./..."
  (cd "$PROJECT_ROOT" && GOWORK=off go vet ./...)
  log_success "vet: OK"
}

function cmd_lint() {
  require_cmd go gofumpt golangci-lint shellcheck actionlint
  log_info "lint: gofumpt -l ."
  local unformatted
  unformatted="$(cd "$PROJECT_ROOT" && gofumpt -l .)"
  if [[ -n "$unformatted" ]]; then
    log_error "gofumpt found unformatted files:"
    printf '  %s\n' "$unformatted" >&2
    exit 1
  fi
  # This repo ships shell as well as Go: ctl.sh itself and review/review.sh, which
  # runs the pull request review agent. An unlinted script is how a defect reaches
  # 20 repositories at once, because review/ is shared by all of them.
  log_info "lint: shellcheck (full strictness)"
  # No mapfile: bash 3.2 on macOS does not have it, and this verb must run on a
  # workstation as well as in the runner image.
  local sh_count
  sh_count="$(cd "$PROJECT_ROOT" && find . -name '*.sh' -not -path './.git/*' | wc -l | tr -d ' ')"
  if [[ "$sh_count" -eq 0 ]]; then
    log_error "no shell scripts found; the discovery is broken, not the repo"
    exit 1
  fi
  log_info "lint: ${sh_count} shell script(s)"
  (cd "$PROJECT_ROOT" && find . -name '*.sh' -not -path './.git/*' -print0 \
     | xargs -0 shellcheck)

  # actionlint is the only linter here that reports a duplicate key: neither yq
  # nor a YAML library does, both keep the last one silently, and a duplicated
  # `timeout-minutes` produced startup failures that attached NO check to the
  # pull request in 3 repositories.
  #
  # This repository's OWN workflows are small — the reusable pr-review workflow
  # that every other repository used to call is retired, replaced by the composite
  # action at review/. What every repository now runs is the GENERATED caller, and
  # that is linted where it is produced: review/action_test.sh renders it with the
  # real generator and runs actionlint over the output. So this pass covers cictl,
  # and that suite covers the fleet.
  log_info "lint: actionlint (.github/workflows)"
  local wf_count
  wf_count="$(cd "$PROJECT_ROOT" && find .github/workflows -type f \( -name '*.yml' -o -name '*.yaml' \) | wc -l | tr -d ' ')"
  if [[ "$wf_count" -eq 0 ]]; then
    log_error "no workflow files found under .github/workflows; the discovery is broken, not the repo"
    exit 1
  fi
  log_info "lint: ${wf_count} workflow file(s)"
  # The config is NAMED rather than discovered, because actionlint finds it from
  # the project root and finds the root from .git, which is a FILE in a worktree.
  # It is also OPTIONAL: a config exists to hold exceptions, and this repository
  # has none left. The two it carried both died with the reusable workflow — the
  # `arc-review` label, which no cictl workflow names now (this repository is
  # PUBLIC and arc-review refuses a public repository), and the ignore for
  # `github.job_workflow_sha`, a property actionlint reported as undefined and
  # which the composite action needs no part of. An ignore that outlives its
  # reason is how a linter stops finding anything.
  local al_config=()
  if [[ -f "$PROJECT_ROOT/.github/actionlint.yaml" ]]; then
    al_config=(-config-file .github/actionlint.yaml)
  fi
  (cd "$PROJECT_ROOT" && find .github/workflows -type f \( -name '*.yml' -o -name '*.yaml' \) -print0 \
     | xargs -0 actionlint -oneline ${al_config[@]+"${al_config[@]}"})

  log_info "lint: go vet ./..."
  (cd "$PROJECT_ROOT" && GOWORK=off go vet ./...)
  log_info "lint: golangci-lint run (shared strict config)"
  if [[ ! -f "$GOLANGCI_CONFIG" ]]; then
    log_error "shared golangci config not found at $GOLANGCI_CONFIG"
    exit 1
  fi
  (cd "$PROJECT_ROOT" && GOWORK=off golangci-lint run --config "$GOLANGCI_CONFIG" ./...)
  log_success "lint: OK"
}

# cmd_gate — the full CI-on-CI gate for cictl: build + lint (fmt/vet/golangci) +
# test -race. This is what a consuming CI tier runs to prove the contract tool
# itself before trusting it to gate the rest.
function cmd_gate() {
  cmd_build
  cmd_lint
  cmd_test
  log_success "gate: cictl is green (build + lint + test -race)"
}

function cmd_install() {
  require_cmd go
  log_info "install: GOWORK=off go install ./cmd/cictl"
  (cd "$PROJECT_ROOT" && GOWORK=off go install ./cmd/cictl)
  log_success "install: cictl installed to \$(go env GOPATH)/bin"
}

# -------- usage --------
function usage() {
  cat <<EOF
Usage: ./ctl.sh <command> [args...]

Commands:
  build          Compile cictl (go build ./...)
  test           The full suite: go test -race ./... then both review suites
  test-review    Only the review/review.sh guard suite (behaviour + mutation)
  test-action    Only the review composite-action suite (behaviour + discrimination)
  fmt            Format the source (gofumpt -w .)
  vet            go vet ./...
  lint           gofumpt check + go vet + golangci-lint + shellcheck + actionlint
  gate           The full CI-on-CI gate: build + lint + test -race
  install        Install the cictl binary (GOWORK=off go install ./cmd/cictl)
  help           Show this message
EOF
}

# -------- dispatcher --------
function main() {
  local cmd="${1:-help}"
  shift || true
  case "$cmd" in
    build)         cmd_build         "$@" ;;
    test)          cmd_test          "$@" ;;
    test-review)   cmd_test_review   "$@" ;;
    test-action)   cmd_test_action   "$@" ;;
    fmt)           cmd_fmt           "$@" ;;
    vet)           cmd_vet           "$@" ;;
    lint)          cmd_lint          "$@" ;;
    gate)          cmd_gate          "$@" ;;
    install)       cmd_install       "$@" ;;
    help|"")       usage ;;
    *)             log_error "unknown command: '$cmd'"; usage; exit 1 ;;
  esac
}

main "$@"
