#!/usr/bin/env bash
#
# Cross-compiles the Go caller for the acceptance image. Node and Python need
# no build step — their source runs as-is, matching how a real deployment
# would find them.
#
# The target architecture is the host's own: e2e-multilang.sh builds the test
# image without a --platform override, so Docker builds for whatever the host
# already is, and this binary has to match it.
set -euo pipefail

cd "$(dirname "$0")"

GOOS=linux GOARCH="$(go env GOARCH)" go build -o go-caller ./go-caller-src
echo "go-caller compiled for linux/$(go env GOARCH) into $(pwd)/go-caller"
