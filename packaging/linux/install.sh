#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER_BIN="${SCRIPT_DIR}/forgehand-installer"

# The installer must be prebuilt; this wrapper never compiles anything.
if [ ! -x "${INSTALLER_BIN}" ]; then
    echo "error: prebuilt installer not found: ${INSTALLER_BIN}" >&2
    echo "build it first, e.g.: go build -o packaging/linux/forgehand-installer ./packaging/linux/installer" >&2
    exit 1
fi

exec "${INSTALLER_BIN}" "$@"
