#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

echo "==> Testing"
go test ./...

echo
echo "==> Vetting"
go vet ./...

echo
echo "==> Race testing"
go test -race ./...

echo
echo "==> Building"
go build -o ./forgehand ./cmd/forgehand

echo
echo "==> Build complete"
./forgehand version
