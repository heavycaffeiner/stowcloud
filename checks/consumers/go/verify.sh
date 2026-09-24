#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

if [ -n "$(go env GOWORK)" ]; then
  echo 'consumer verification must run outside a Go workspace' >&2
  exit 1
fi

expected=(
  'github.com/stowcloud/durablefs v0.1.0'
  'github.com/stowcloud/namesearch v0.1.0'
  'github.com/stowcloud/sandbox-worker v0.1.0'
  'github.com/stowcloud/storage v0.1.0'
  'github.com/stowcloud/transfer v0.2.0'
  'github.com/stowcloud/veracrypt v0.1.2'
)

for module in "${expected[@]}"; do
  if ! grep -Fqx "$module" <(go list -m -mod=readonly all); then
    echo "required public module is not resolved at the pinned release: $module" >&2
    exit 1
  fi
done

if grep -Eq '^[[:space:]]*replace([[:space:]]|\()' go.mod; then
  echo 'consumer module must not use local replacements' >&2
  exit 1
fi

go vet ./...
go test ./...
