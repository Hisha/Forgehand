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
build_commit="$(git rev-parse HEAD)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
go build \
  -ldflags "-X github.com/Hisha/Forgehand/internal/version.Commit=${build_commit} -X github.com/Hisha/Forgehand/internal/version.BuildDate=${build_date}" \
  -o ./forgehand ./cmd/forgehand

echo
echo "==> Build complete"
./forgehand version
echo "If a user service is running a different build, open the TUI to inspect and safely resolve the mismatch."

echo
echo "==>restart daemon"
systemctl --user restart forgehand.service

systemctl --user status forgehand.service --no-pager
