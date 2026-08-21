#!/usr/bin/env bash
#
# Multi-container acceptance test.
#
# Every service runs in a container of its own — four Java nodes, a Node.js, a
# Python and a Go caller, and an nginx and an Apache httpd reverse proxy in
# front of the chain: nine pid, mount and network namespaces, with apm2go in a
# tenth, from the image it ships as. Nothing is shared between any of them
# except the kernel, and none of the workload containers contains apm2go, an
# agent jar, or a line of tracing code.
#
# build/e2e-container.sh already asserts this for one Java workload. What is new
# here is everything that only appears once services are spread out:
#
#   * a trace that has to cross container boundaries to stay one trace, rather
#     than becoming seven single-service traces;
#   * eBPF instrumentation of a process in a network namespace apm2go is not in,
#     where the listening port apm2go discovered is not a port apm2go can
#     connect to;
#   * the language of each service, which the UI badges every service name with
#     and which therefore has to survive ingest for every runtime here;
#   * a web server, which is neither an interpreter nor a Go binary and runs as
#     a master with a pool of workers that all hold the same listening socket —
#     so the one thing that must not happen is the same server being reported
#     once per worker, under a pid that its next reload replaces.
#
# The two web servers are not asserted identically, because measured directly
# they do not behave identically. Both are discovered, named, and produce a
# server span for the inbound request and a client span for the proxied call.
# Neither carries trace context reliably, and the two fail differently:
#
#   nginx  never injects a traceparent into the request it forwards, so the
#          backend always starts a trace of its own. Confirmed by reading the
#          headers that arrive upstream, not inferred from missing spans.
#
#   httpd  injects it for some requests and then stops. Sampled every two
#          minutes against a steady 20 requests/minute: about a third of
#          requests joined for the first twelve minutes, and none after that,
#          for the remaining sixteen minutes of the run. The shape fits OBI
#          attaching to individual Apache children rather than to the server —
#          requests landing on an attached child are joined, and the event MPM
#          retires those children in the ordinary course of scaling its pool.
#
# So the assertion below is deliberately weak: it checks that httpd joins *at
# all*, near the start of the run, which is a real property whose loss would be
# a regression worth catching. It is not evidence that httpd is fully covered,
# and it must not be strengthened into a proportion or a steady state — both
# would be asserting something this arrangement does not do. Both defects live
# in the eBPF layer's context propagation, not in apm2go; PHP fails the same
# way (see build/e2e-multilang.sh).
#
# apm2go is queried from inside its own container: with --network=host it lives
# in the Linux VM's network namespace, which on macOS the host cannot reach.
set -euo pipefail

cd "$(dirname "$0")/.."

APM2GO_IMAGE="${APM2GO_IMAGE:-apm2go:latest}"
WORKLOAD_IMAGE="${WORKLOAD_IMAGE:-apm2go-workload:latest}"

# KEEP=1 leaves every container running when the script finishes, pass or fail,
# and publishes the UI. The test tears its arrangement down by default because
# an acceptance test that leaves eight containers and a network behind is one
# nobody runs twice; but the arrangement is also the only place this product's
# central claim is visible as a whole, so it has to be possible to walk up to it
# and look. UI_PORT is where it is published — see the forwarder below for why
# apm2go's own container cannot publish anything itself.
KEEP="${KEEP:-}"
UI_PORT="${UI_PORT:-18600}"

NETWORK="apm2go-e2e-$$"
AGENT="apm2go-e2e-agent-$$"
# Container names double as hostnames on the network above, which is what lets
# one service address another without an ip address being known in advance.
SERVICES=(inventory payments orders gateway node-caller py-caller go-caller nginx httpd traffic)

FORWARDER="apm2go-e2e-ui-$$"

CONTAINERS=("${AGENT}" "${FORWARDER}")
for service in "${SERVICES[@]}"; do CONTAINERS+=("${service}-$$"); done

cleanup() {
    if [ -n "${KEEP}" ]; then
        echo
        echo "KEEP is set, so everything is still running:"
        docker ps --filter "name=-$$" --format '    {{.Names}}\t{{.Image}}\t{{.Status}}'
        echo
        echo "    UI:       http://127.0.0.1:${UI_PORT}"
        echo "    Tear down: docker rm -f \$(docker ps -aq --filter name=-$$) && docker network rm ${NETWORK}"
        return
    fi
    docker rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
    docker network rm "${NETWORK}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

api() { docker exec "${AGENT}" curl -sf "http://127.0.0.1:8080/api/v1/$1"; }

fail() {
    echo "FAIL: $1"
    echo "--- apm2go logs ---"
    docker logs "${AGENT}" 2>&1 | tail -30
    exit 1
}

# jq is not assumed; python3 is already a dependency of every other test here.
json() { python3 -c "$1"; }

echo "==> Compiling the workloads"
./build/chain/build-app.sh >/dev/null
./build/multilang/build-app.sh >/dev/null

echo "==> Building the workload image (no apm2go in it)"
docker build -q -f build/Dockerfile.workload -t "${WORKLOAD_IMAGE}" . >/dev/null

docker network create "${NETWORK}" >/dev/null

CHAIN_CP='/opt/chain/classes:/opt/chain/lib/*'

# One Java node per container. The classpath wildcard is expanded by the JVM,
# so no shell is needed between docker and the process being traced.
start_java() {
    local name=$1 port=$2 path=$3 downstream=$4 selfloop=$5 deadlock=$6
    docker run -d --name "${name}-$$" --network "${NETWORK}" --network-alias "${name}" \
        "${WORKLOAD_IMAGE}" \
        java -cp "${CHAIN_CP}" \
        "-Dspring.application.name=${name}-service" \
        "-Dchain.port=${port}" \
        "-Dchain.path=${path}" \
        "-Dchain.downstream=${downstream}" \
        "-Dchain.selfloop=${selfloop}" \
        "-Dchain.deadlock=${deadlock}" \
        ChainNode >/dev/null
}

echo "==> Starting nine services, one container each, plus a traffic driver"
# Leaves first, so nothing calls a service that is not listening yet.
start_java inventory 8083 /api/inventory "" "" false
start_java payments  8084 /api/payments  "" "" true
sleep 3
start_java orders    8082 /api/orders    "http://inventory:8083/api/inventory" "" false
sleep 3
# Only the entry point drives the Java chain, so it exercises itself without a
# load generator outside the containers.
start_java gateway   8081 /api/gateway \
    "http://orders:8082/api/orders,http://payments:8084/api/payments" \
    "http://127.0.0.1:8081/api/gateway" false
sleep 5

GATEWAY="http://gateway:8081/api/gateway"
docker run -d --name "node-caller-$$" --network "${NETWORK}" --network-alias node-caller \
    -e CALLER_PORT=8090 -e "CALLER_DOWNSTREAM=${GATEWAY}" \
    "${WORKLOAD_IMAGE}" node /opt/multilang/node-caller.js >/dev/null
docker run -d --name "py-caller-$$" --network "${NETWORK}" --network-alias py-caller \
    -e CALLER_PORT=8095 -e "CALLER_DOWNSTREAM=${GATEWAY}" \
    "${WORKLOAD_IMAGE}" python3 /opt/multilang/py-caller.py >/dev/null
docker run -d --name "go-caller-$$" --network "${NETWORK}" --network-alias go-caller \
    -e CALLER_PORT=8096 -e "CALLER_DOWNSTREAM=${GATEWAY}" \
    "${WORKLOAD_IMAGE}" /opt/multilang/go-caller >/dev/null

# The two web servers, each proxying to the Java gateway. Both run in the
# foreground with their packaged worker pools, so each is a master plus several
# workers sharing one listening socket — and each listens on its own port as
# well as the distribution's default :80.
docker run -d --name "nginx-$$" --network "${NETWORK}" --network-alias nginx \
    "${WORKLOAD_IMAGE}" nginx -g 'daemon off;' >/dev/null
docker run -d --name "httpd-$$" --network "${NETWORK}" --network-alias httpd \
    "${WORKLOAD_IMAGE}" httpd -DFOREGROUND >/dev/null

# Nothing calls a reverse proxy on its own the way the Java gateway and the
# three callers drive themselves, so one container does it for both. Traffic
# has to be continuous rather than a burst at assertion time: a service with a
# single point in an otherwise empty window reads as broken even when it is not.
docker run -d --name "traffic-$$" --network "${NETWORK}" \
    "${WORKLOAD_IMAGE}" bash -c '
    while true; do
        curl -sf --max-time 5 http://nginx:8100/ >/dev/null 2>&1 || true
        curl -sf --max-time 5 http://httpd:8101/ >/dev/null 2>&1 || true
        sleep 2
    done' >/dev/null

sleep 10
for service in "${SERVICES[@]}"; do
    docker inspect -f '{{.State.Running}}' "${service}-$$" 2>/dev/null | grep -q true \
        || fail "${service} exited before apm2go started: $(docker logs "${service}-$$" 2>&1 | tail -5)"
done
echo "    ${#SERVICES[@]} containers up, serving traffic, with no apm2go anywhere in them"

# The pids each Java service started with, read from its own namespace. A pid
# that changes later means apm2go restarted the process, which is the one thing
# it exists not to do. Kept in a file rather than an associative array: macOS
# ships bash 3.2, which has none, and this script has to run where it is run.
PID_FILE="$(mktemp)"
trap 'rm -f "${PID_FILE}"; cleanup' EXIT
for service in inventory payments orders gateway; do
    echo "${service} $(docker exec "${service}-$$" pgrep -x java)" >> "${PID_FILE}"
done

echo "==> Starting apm2go alone in its own container, in the host's namespaces"
docker run -d --name "${AGENT}" \
    --pid=host --network=host --privileged \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    --entrypoint bash "${APM2GO_IMAGE}" -c '
mkdir -p /var/lib/apm2go
cat > /tmp/cfg.yaml <<YAML
data_dir: /var/lib/apm2go
api:
  addr: 0.0.0.0:8080
discovery:
  min_uptime: 1s
receiver:
  container_bind: auto
YAML
exec apm2go run --config /tmp/cfg.yaml
' >/dev/null

echo "==> Waiting for the API"
for _ in $(seq 1 60); do
    api health >/dev/null 2>&1 && break
    sleep 1
done
api health >/dev/null || fail "the API never became reachable"

if [ -n "${KEEP}" ]; then
    # apm2go runs with --network=host, which is mutually exclusive with -p:
    # there is no port to publish, because it is already bound directly in the
    # Linux VM's own network namespace — which, on macOS, the host cannot reach.
    # This forwards a published port to it across the default bridge, the same
    # route the workload containers' telemetry takes in the other direction.
    GATEWAY="$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}')"
    docker run -d --name "${FORWARDER}" -p "${UI_PORT}:8080" alpine/socat \
        tcp-listen:8080,fork,reuseaddr "tcp-connect:${GATEWAY}:8080" >/dev/null
    echo "    UI published on http://127.0.0.1:${UI_PORT}"
fi

echo "==> Waiting for the four Java services to be attached across the boundary"
attached=0
for _ in $(seq 1 60); do
    attached="$(api jvms | json "
import json, sys
entries = json.load(sys.stdin)
print(sum(1 for e in entries if e['state'] == 'attached' and e['jvm']['in_container']))
" 2>/dev/null || echo 0)"
    [ "${attached}" -ge 4 ] && break
    sleep 2
done
[ "${attached}" -ge 4 ] || fail "only ${attached} of 4 containerized JVMs reached the attached state"
echo "    4 JVMs attached, each in a container of its own"

echo "==> Checking that no target was restarted"
while read -r service before; do
    now="$(docker exec "${service}-$$" pgrep -x java)"
    [ "${now}" = "${before}" ] || fail "${service} changed pid from ${before} to ${now}"
done < "${PID_FILE}"
echo "    every pid unchanged: nothing was restarted to be instrumented"

echo "==> Waiting for all nine services to report"
for _ in $(seq 1 48); do
    missing="$(api "services?from=-15m&to=now" | json "
import json, sys
want = {'gateway-service', 'orders-service', 'inventory-service', 'payments-service',
        'node-caller', 'py-caller', 'go-caller', 'nginx', 'httpd'}
have = {s['service'] for s in json.load(sys.stdin)['services']}
print(','.join(sorted(want - have)))
" 2>/dev/null || echo "api-unavailable")"
    [ -z "${missing}" ] && break
    sleep 5
done
[ -z "${missing}" ] || fail "these services never reported: ${missing}"
echo "    all nine reporting, from nine separate containers"

echo "==> Checking the runtime recorded for each service"
api "services?from=-15m&to=now" | json "
import json, sys
want = {'gateway-service': 'java', 'orders-service': 'java',
        'inventory-service': 'java', 'payments-service': 'java',
        'node-caller': 'nodejs', 'py-caller': 'python', 'go-caller': 'go',
        # Not 'c'. OBI can watch a native binary but cannot read a runtime out
        # of it, so what fills these in is apm2go's own discovery — which is
        # the only thing that ever knew the process was a web server at all.
        'nginx': 'nginx', 'httpd': 'httpd'}
have = {s['service']: s.get('runtime', '') for s in json.load(sys.stdin)['services']}
wrong = {k: (have.get(k), v) for k, v in want.items() if have.get(k) != v}
if wrong:
    for service, (got, expected) in sorted(wrong.items()):
        print(f'  {service}: runtime={got!r}, want {expected!r}')
    sys.exit(1)
" || fail "a service reported the wrong runtime (see above)"
echo "    every service named its own runtime"

echo "==> Checking that each web server was reported once, not once per worker"
for server in nginx httpd; do
    workers="$(docker exec "${server}-$$" bash -c "pgrep -c -x ${server}")"
    [ "${workers}" -ge 2 ] || fail "${server} is running ${workers} process(es); this check needs a worker pool to mean anything"

    reported="$(api "services?from=-15m&to=now" | json "
import json, sys
names = [s['service'] for s in json.load(sys.stdin)['services']]
# A per-worker target would show up either as the name repeated or — once
# disambiguate() has had its say — as the name with a port appended.
print(sum(1 for n in names if n == '${server}' or n.startswith('${server}-')))
")"
    [ "${reported}" = "1" ] || fail "${server} has ${workers} processes and reported as ${reported} services, want exactly 1"
    echo "    ${server}: ${workers} processes, 1 service"
done

echo "==> Checking what each reverse proxy contributes to a trace"
# httpd carries the trace across for some requests early in a run: one waterfall
# from the proxy through all four Java services. See the header for how long
# that lasts, and why this asks only whether it happens at all.
joined=0
for _ in $(seq 1 20); do
    joined="$(api "traces?from=-15m&to=now&limit=300" | json "
import json, sys
traces = json.load(sys.stdin)['traces']
print(sum(1 for t in traces if t['root_service'] == 'httpd' and t['service_count'] >= 5))
" 2>/dev/null || echo 0)"
    [ "${joined}" -gt 0 ] && break
    sleep 5
done
[ "${joined}" -gt 0 ] || fail "no request through httpd produced one trace across the Java chain"
echo "    httpd: ${joined} traces carried through to all four Java services (early-run only; see header)"

# nginx does not, and the useful thing is still there: its own server span and
# the client span for the call it made. A regression that lost either would be
# invisible in a check that only counted services per trace.
kinds="$(api "traces?from=-15m&to=now&limit=300" | json "
import json, sys, urllib.request
traces = [t for t in json.load(sys.stdin)['traces'] if t['root_service'] == 'nginx']
print(traces[0]['trace_id'] if traces else '')
")"
[ -n "${kinds}" ] || fail "nginx produced no traces at all"
api "traces/${kinds}" | json "
import json, sys
spans = json.load(sys.stdin)['spans']
mine = [s for s in spans if s['service'] == 'nginx']
# kind 2 is server, kind 3 is client.
if not any(s['kind'] == 2 for s in mine):
    print('  no server span'); sys.exit(1)
if not any(s['kind'] == 3 for s in mine):
    print('  no client span for the proxied call'); sys.exit(1)
" || fail "nginx's trace is missing a span it should have (see above)"
echo "    nginx: server and client spans present; its trace does not join the backend's (known, see header)"

echo "==> Checking that a trace spans the containers it passed through"
multi=0
for _ in $(seq 1 20); do
    multi="$(api "traces?from=-15m&to=now&limit=200" | json "
import json, sys
traces = json.load(sys.stdin)['traces']
print(sum(1 for t in traces if t['service_count'] > 1 and t['root_service'] in
          ('node-caller', 'py-caller', 'go-caller')))
" 2>/dev/null || echo 0)"
    [ "${multi}" -gt 0 ] && break
    sleep 5
done
[ "${multi}" -gt 0 ] || fail "no trace entered from a non-Java container and reached the Java chain"
echo "    ${multi} traces cross from a non-Java container into the Java chain"

echo "==> Checking that apm2go instrumented each service exactly once"
api "services?from=-15m&to=now" | json "
import json, sys
services = json.load(sys.stdin)['services']
names = [s['service'] for s in services]
duplicates = {n for n in names if names.count(n) > 1}
# A Java service watched by both its agent and eBPF would report under two
# names, or the same name twice. Either is the failure this looks for.
strays = [n for n in names if n.endswith('-service') and n not in
          ('gateway-service', 'orders-service', 'inventory-service', 'payments-service')]
if duplicates or strays:
    print(f'  duplicates={sorted(duplicates)} strays={sorted(strays)}')
    sys.exit(1)
" || fail "a service was instrumented twice (see above)"
echo "    no service reported twice"

echo
echo "PASS: apm2go, alone in its own container, discovered and instrumented"
echo "      nine services in nine other containers — four languages and two"
echo "      web servers — without restarting any of them."
