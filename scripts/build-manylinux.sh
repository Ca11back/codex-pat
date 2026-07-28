#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${VERSION:-0.1.5}"
goarch="${GOARCH:-$(go env GOARCH)}"
output="${OUTPUT:-dist/codex-pat-v${version}.so}"

python3 "${repo_root}/scripts/release.py" validate-version "${version}" >/dev/null

case "${goarch}" in
  amd64)
    image="quay.io/pypa/manylinux2014_x86_64"
    machine_pattern='x86_64'
    ;;
  arm64)
    image="quay.io/pypa/manylinux2014_aarch64"
    machine_pattern='aarch64|arm64'
    ;;
  *)
    echo "unsupported manylinux architecture: ${goarch}" >&2
    exit 1
    ;;
esac

go_root="$(go env GOROOT)"
mkdir -p "${repo_root}/$(dirname "${output}")"

docker run --rm \
  --volume "${repo_root}:/workspace" \
  --volume "${go_root}:/opt/go:ro" \
  --workdir /workspace \
  --env "EXPECTED_MACHINE=${machine_pattern}" \
  --env "GOARCH=${goarch}" \
  --env "OUTPUT=${output}" \
  --env "VERSION=${version}" \
  --env "GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}" \
  "${image}" \
  bash -euo pipefail -c '
    [[ "$(uname -m)" =~ ${EXPECTED_MACHINE} ]]
    export PATH=/opt/go/bin:${PATH}
    export CGO_ENABLED=1 GOOS=linux HOME=/tmp/codex-pat-home
    export GOCACHE=/tmp/codex-pat-go-build GOMODCACHE=/tmp/codex-pat-go-mod
    mkdir -p "${HOME}" "$(dirname "${OUTPUT}")"
    go build -trimpath -buildvcs=false -buildmode=c-shared \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "${OUTPUT}" ./cmd/codex-pat
    chmod 0755 "${OUTPUT}"
  '

python3 "${repo_root}/scripts/release.py" verify-library \
  --library "${repo_root}/${output}" --goos linux --goarch "${goarch}" \
  --glibc-max 2.17
