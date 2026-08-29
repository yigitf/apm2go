#!/usr/bin/env bash
#
# The test suite, run inside the build container by `make test`.
#
# It is two passes, not one, and that is not tidiness. internal/attach verifies
# the privilege drop that apm2go performs before touching a target JVM, and that
# drop is built on Go's AllThreadsSyscall, which returns ENOTSUP the instant cgo
# is linked into the binary. Every other package needs CGO_ENABLED=1, for the
# DuckDB driver. So the attach package is tested cgo-free and the rest is not —
# the same split that makes apm2go-attach-helper a separate binary in
# production.
#
# The container also has to be given CAP_SYS_PTRACE, which is not in Docker's
# default set; the Makefile does that.
set -euo pipefail

source /opt/rh/gcc-toolset-12/enable

echo "==> gofmt"
gofmt -l . | tee /dev/stderr | (! read)

echo "==> go vet"
go vet ./...

echo "==> go test (cgo)"
CGO_ENABLED=1 go test "$@" $(go list ./... | grep -v '/internal/attach$')

echo "==> go test (cgo-free: internal/attach)"
CGO_ENABLED=0 go test "$@" ./internal/attach/...
