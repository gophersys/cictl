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
REPO_ROOT="$(git -C "$PROJECT_ROOT" rev-parse --show-toplevel 2>/dev/null || echo "$PROJECT_ROOT")"
GOLANGCI_CONFIG="$REPO_ROOT/libs/.golangci.yml"

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
  log_success "test: OK"
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
  require_cmd go gofumpt golangci-lint
  log_info "lint: gofumpt -l ."
  local unformatted
  unformatted="$(cd "$PROJECT_ROOT" && gofumpt -l .)"
  if [[ -n "$unformatted" ]]; then
    log_error "gofumpt found unformatted files:"
    printf '  %s\n' "$unformatted" >&2
    exit 1
  fi
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
  build        Compile cictl (go build ./...)
  test         Run the test suite with the race detector (go test -race ./...)
  fmt          Format the source (gofumpt -w .)
  vet          go vet ./...
  lint         gofumpt check + go vet + golangci-lint (shared strict config)
  gate         The full CI-on-CI gate: build + lint + test -race
  install      Install the cictl binary (GOWORK=off go install ./cmd/cictl)
  help         Show this message
EOF
}

# -------- dispatcher --------
function main() {
  local cmd="${1:-help}"
  shift || true
  case "$cmd" in
    build)    cmd_build   "$@" ;;
    test)     cmd_test    "$@" ;;
    fmt)      cmd_fmt     "$@" ;;
    vet)      cmd_vet     "$@" ;;
    lint)     cmd_lint    "$@" ;;
    gate)     cmd_gate    "$@" ;;
    install)  cmd_install "$@" ;;
    help|"")  usage ;;
    *)        log_error "unknown command: '$cmd'"; usage; exit 1 ;;
  esac
}

main "$@"
