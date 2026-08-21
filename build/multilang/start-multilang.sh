#!/usr/bin/env bash
#
# Starts three non-Java services, each calling into the already-running Java
# chain (see build/chain/start-chain.sh, started before this):
#
#   node-caller (Node.js) ──┐
#   py-caller   (Python)  ──┼──→ gateway (Java, via the existing chain)
#   go-caller   (Go)       ──┘
#
# None of the three carry tracing code. Whatever apm2go reports about them, it
# learned by watching already-running processes from outside — the same claim
# build/chain/start-chain.sh makes for the Java side, extended to the
# languages that cannot be attached to at all, and so depend entirely on that
# external observation actually working.
#
# Each drives its own traffic every 3 seconds (CALLER_SELFLOOP_MS /
# CALLER_SELFLOOP_SECONDS, on by default), the same way the Java chain's
# gateway does. Without it a service's time series is whatever a test script
# happened to send at the moments it happened to send it — one lonely point in
# an otherwise empty window, which reads as broken even when nothing is.
set -euo pipefail

MULTILANG_DIR="${MULTILANG_DIR:-/opt/multilang}"
LOG_DIR="${LOG_DIR:-/var/log/multilang}"
DOWNSTREAM="${MULTILANG_DOWNSTREAM:-http://127.0.0.1:8081/api/gateway}"

mkdir -p "${LOG_DIR}"

CALLER_PORT=8090 CALLER_DOWNSTREAM="${DOWNSTREAM}" \
    node "${MULTILANG_DIR}/node-caller.js" > "${LOG_DIR}/node-caller.log" 2>&1 &
echo "node-caller started as pid $!"

CALLER_PORT=8095 CALLER_DOWNSTREAM="${DOWNSTREAM}" \
    python3 "${MULTILANG_DIR}/py-caller.py" > "${LOG_DIR}/py-caller.log" 2>&1 &
echo "py-caller started as pid $!"

CALLER_PORT=8096 CALLER_DOWNSTREAM="${DOWNSTREAM}" \
    "${MULTILANG_DIR}/go-caller" > "${LOG_DIR}/go-caller.log" 2>&1 &
echo "go-caller started as pid $!"
