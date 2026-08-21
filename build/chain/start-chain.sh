#!/usr/bin/env bash
#
# Starts a four-node service chain: gateway calls orders and payments, orders
# calls inventory, and every node queries a database of its own.
#
#   gateway ──┬─→ orders ──→ inventory
#             └─→ payments
#
# Each node is an ordinary Jetty servlet application containing no tracing code
# whatsoever. They are started, and left running, before apm2go exists — so
# whatever apm2go reports about them, it learned by attaching to software that
# was already serving traffic.
set -euo pipefail

JAVA_BIN="${JAVA_BIN:-java}"
CHAIN_DIR="${CHAIN_DIR:-/opt/chain}"
LOG_DIR="${LOG_DIR:-/var/log/chain}"

CP="${CHAIN_DIR}/classes:$(ls "${CHAIN_DIR}"/lib/*.jar | tr '\n' ':')"
mkdir -p "${LOG_DIR}"

start_node() {
    local name=$1 port=$2 path=$3 downstream=$4 selfloop=$5 deadlock=${6:-false}
    "${JAVA_BIN}" -cp "${CP}" \
        -Dspring.application.name="${name}" \
        -Dchain.port="${port}" \
        -Dchain.path="${path}" \
        -Dchain.downstream="${downstream}" \
        -Dchain.selfloop="${selfloop}" \
        -Dchain.deadlock="${deadlock}" \
        ChainNode > "${LOG_DIR}/${name}.log" 2>&1 &
    echo "${name} started as pid $!"
}

# Leaves first, so the entry point never calls a node that is not yet listening.
start_node inventory-service 8083 /api/inventory "" ""
# payments deadlocks two of its threads on purpose. It keeps serving requests
# normally around them, which is the point: the fault is invisible from the
# outside and only a thread dump reveals it.
start_node payments-service  8084 /api/payments  "" "" true
sleep 2
start_node orders-service    8082 /api/orders    "http://127.0.0.1:8083/api/inventory" ""
sleep 2
# Only the entry point drives traffic, so the chain exercises itself without an
# external load generator.
start_node gateway-service   8081 /api/gateway \
    "http://127.0.0.1:8082/api/orders,http://127.0.0.1:8084/api/payments" \
    "http://127.0.0.1:8081/api/gateway"
