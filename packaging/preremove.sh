#!/bin/sh
# Runs before the package is removed.
set -e

if command -v systemctl >/dev/null 2>&1; then
    # Only stop on an actual removal. On RPM an upgrade passes 1 here and on
    # DEB it passes "upgrade"; stopping in those cases would interrupt a
    # service that postinstall is about to restart anyway.
    case "${1:-}" in
        0|remove|purge)
            systemctl stop apm2go >/dev/null 2>&1 || true
            systemctl disable apm2go >/dev/null 2>&1 || true
            ;;
    esac
fi
