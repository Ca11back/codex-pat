#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

if [[ -z "${GOCACHE:-}" ]]; then
  default_go_cache="$(go env GOCACHE)"
  if [[ ! -w "${default_go_cache}" ]]; then
    export GOCACHE="${TMPDIR:-/tmp}/codex-pat-go-build-cache"
    mkdir -p "${GOCACHE}"
  fi
fi

if [[ -z "${GOMODCACHE:-}" ]]; then
  default_module_cache="$(go env GOMODCACHE)"
  if [[ ! -w "${default_module_cache}" ]]; then
    export GOMODCACHE="${TMPDIR:-/tmp}/codex-pat-go-module-cache"
    mkdir -p "${GOMODCACHE}"
  fi
fi

exec go test -tags=integration ./integration -count=1 -v "$@"
