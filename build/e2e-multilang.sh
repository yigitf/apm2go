#!/usr/bin/env bash
#
# Multi-language acceptance test.
#
# Installs apm2go from its RPM on a clean Rocky Linux image where the Java
# chain and three non-Java callers (Node.js, Python, Go) are already running,
# and verifies the claim M8 exists for: a plaintext HTTP request from a Node,
# Python or Go service into the Java chain produces one trace spanning every
# service it touched, with no restart and no code in any of them apm2go did
# not put there — plus the two failure modes that matter more than the happy
# path: a Java service must never be instrumented twice (once by its own
# agent, once by eBPF observing it too), and killing the eBPF subprocess must
# never take the Java side down with it.
#
# PHP is deliberately not exercised here. Measured directly, OBI recognises a
# PHP process and captures its inbound request but never correlates the
# outbound call to it, so a PHP node would only ever produce an isolated,
# single-service trace — asserting that here would either assert a negative
# result forever or need loosening the moment OBI's PHP support improves.
# That result is documented in the development plan instead.
set -euo pipefail

cd "$(dirname "$0")/.."

# The newest package for this host's architecture. Two things make the obvious
# `ls | head -1` wrong: dist/ accumulates a package per build, so the
# alphabetical winner is whichever commit hash sorts lowest — an arbitrary old
# build — and every build produces both architectures, so half of what is there
# cannot be installed in the image at all. Either mistake fails as a regression
# in code that is fine, which is the most expensive kind of test failure.
# uname -m is the host's name for the architecture, which is not the name RPM
# uses for it: macOS says arm64 where the package is tagged aarch64.
case "$(uname -m)" in
    arm64 | aarch64) RPM_ARCH=aarch64 ;;
    x86_64 | amd64)  RPM_ARCH=x86_64 ;;
    *)               RPM_ARCH="$(uname -m)" ;;
esac
RPM_FILE="${RPM_FILE:-$(ls -1t "dist/apm2go-"*".${RPM_ARCH}.rpm" 2>/dev/null | head -1 | xargs -n1 basename || true)}"
if [ -z "${RPM_FILE}" ]; then
    echo "No RPM found in dist/. Run ./build/package.sh first." >&2
    exit 1
fi

CONTAINER="apm2go-e2e-multilang-$$"
cleanup() { docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> Compiling the chain and multi-language workloads"
./build/chain/build-app.sh >/dev/null
./build/multilang/build-app.sh >/dev/null

echo "==> Building acceptance image from ${RPM_FILE}"
docker build -q -f build/Dockerfile.rpmtest-multilang --build-arg "RPM_FILE=${RPM_FILE}" \
    -t apm2go-rpmtest-multilang:latest . >/dev/null

echo "==> Starting the chain and non-Java callers, then apm2go against them"
# --privileged is what eBPF instrumentation needs (CAP_BPF, CAP_PERFMON); the
# Java side of this same test runs identically without it.
docker run -d --name "${CONTAINER}" --privileged -p 18180:8080 apm2go-rpmtest-multilang:latest bash -c '
    sed -i "s|min_uptime: 10s|min_uptime: 1s|" /etc/apm2go/config.yaml
    exec apm2go run --config /etc/apm2go/config.yaml
' >/dev/null

api() { curl -sf "http://127.0.0.1:18180/api/v1/$1"; }
fail() { echo "FAIL: $1"; docker logs "${CONTAINER}" 2>&1 | tail -40; exit 1; }

echo "==> Waiting for the API"
for _ in $(seq 1 60); do
    if api health >/dev/null 2>&1; then break; fi
    sleep 1
done
api health >/dev/null || fail "the API never became reachable"

echo "==> Checking that all four Java JVMs were attached, the ordinary way"
for _ in $(seq 1 60); do
    attached="$(api jvms | python3 -c "
import json, sys
print(sum(1 for e in json.load(sys.stdin) if e['state'] == 'attached'))
" 2>/dev/null || echo 0)"
    [ "${attached}" -ge 4 ] && break
    sleep 2
done
[ "${attached}" -ge 4 ] || fail "only ${attached} of 4 JVMs reached the attached state"
echo "    4 JVMs attached via the ordinary Java path"

echo "==> Driving traffic through the three non-Java callers"
for _ in $(seq 1 5); do
    curl -sf "http://127.0.0.1:18180" >/dev/null 2>&1 || true
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8090/ >/dev/null 2>&1 || true
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8095/ >/dev/null 2>&1 || true
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8096/ >/dev/null 2>&1 || true
    sleep 3
done

echo "==> Checking that each non-Java service was discovered and named"
for _ in $(seq 1 20); do
    found="$(api 'services?from=-10m' | python3 -c "
import json, sys
names = {s['service'] for s in json.load(sys.stdin)['services']}
print(len({'node-caller', 'py-caller', 'go-caller'} & names))
" 2>/dev/null || echo 0)"
    [ "${found}" -ge 3 ] && break
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8090/ >/dev/null 2>&1 || true
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8095/ >/dev/null 2>&1 || true
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8096/ >/dev/null 2>&1 || true
    sleep 3
done
[ "${found}" -ge 3 ] || fail "expected node-caller, py-caller and go-caller as services, found ${found}/3"
echo "    node-caller, py-caller and go-caller all discovered and named automatically"

echo "==> Checking that each one's trace joins the whole Java chain"
# This is the assertion the whole design is for: a plaintext HTTP request
# observed by eBPF, in a language apm2go never attached to, continuing into
# spans the OpenTelemetry Java agent produced — proof the traceparent one
# instrumentation method writes is the one the other reads.
# Bash 3.2, which macOS still ships, has no associative arrays.
caller_port() {
    case "$1" in
        node-caller) echo 8090 ;;
        py-caller) echo 8095 ;;
        go-caller) echo 8096 ;;
    esac
}

for svc in node-caller py-caller go-caller; do
    joined=0
    for _ in $(seq 1 20); do
        # A request per retry, not just a re-check: the first requests may
        # have landed before OBI finished attaching to a freshly (re)started
        # target, and this loop's job is to keep trying until one lands after.
        docker exec "${CONTAINER}" curl -sf "http://127.0.0.1:$(caller_port "${svc}")/" >/dev/null 2>&1 || true
        sleep 2
        joined="$(api "traces?from=-10m&service=${svc}&limit=5" | python3 -c "
import json, sys
traces = json.load(sys.stdin)['traces']
print(sum(1 for t in traces if t['service_count'] >= 5))
" 2>/dev/null || echo 0)"
        [ "${joined}" -gt 0 ] && break
    done
    [ "${joined}" -gt 0 ] || fail "${svc}'s trace never joined the Java chain (service_count >= 5)"
    echo "    ${svc} -> Java chain: one waterfall"
done

echo "==> Checking that no Java service was instrumented twice"
# A Java process matched by eBPF's own process scan in addition to its own
# agent would double every span it produces. apm2go's discovery only ever
# recognises node, python and php-fpm executables by name and Go binaries by
# their build metadata — "java" appears in neither — so the check here is
# that the trace shape is still exactly what build/e2e.sh already established
# for an unmodified four-node chain: one server span per Java service per hop.
trace_id="$(api 'traces?from=-10m&service=node-caller&limit=5' | python3 -c "
import json, sys
traces = json.load(sys.stdin)['traces']
multi = [t for t in traces if t['service_count'] >= 5]
print(multi[0]['trace_id'] if multi else '')
")"
[ -n "${trace_id}" ] || fail "could not re-fetch a joined trace to inspect its shape"
api "traces/${trace_id}" | python3 -c "
import json, sys
trace = json.load(sys.stdin)
spans = trace['spans']
by_service = {}
for s in spans:
    if s['kind'] == 2:  # SERVER
        by_service[s['service']] = by_service.get(s['service'], 0) + 1
for svc in ('gateway-service', 'orders-service', 'inventory-service'):
    n = by_service.get(svc, 0)
    if n != 1:
        raise SystemExit('%s has %d server spans in this trace, want exactly 1 (double instrumentation looks like 2)' % (svc, n))
print('    exactly one server span per Java hop: no double instrumentation')
" || fail "the trace shape suggests a Java service was instrumented twice"

echo "==> Checking that killing the eBPF subprocess does not touch the Java side"
obi_pid="$(docker exec "${CONTAINER}" pgrep obi | head -1)"
[ -n "${obi_pid}" ] || fail "no obi process was running to kill"
docker exec "${CONTAINER}" kill -9 "${obi_pid}"
sleep 2
docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8081/api/gateway >/dev/null \
    || fail "the Java chain stopped serving after the eBPF subprocess was killed"
still_attached="$(api jvms | python3 -c "
import json, sys
print(sum(1 for e in json.load(sys.stdin) if e['state'] == 'attached'))
")"
[ "${still_attached}" -ge 4 ] || fail "a JVM lost its attached state after the eBPF subprocess died"
echo "    Java chain kept serving; all 4 JVMs still attached"

echo "==> Checking that the eBPF subprocess restarts on its own"
for _ in $(seq 1 15); do
    docker exec "${CONTAINER}" pgrep obi >/dev/null 2>&1 && break
    sleep 2
done
docker exec "${CONTAINER}" pgrep obi >/dev/null 2>&1 || fail "the eBPF subprocess never came back after being killed"
echo "    eBPF subprocess self-healed"

echo "==> Checking that CPU and memory are reported per non-Java process"
# These come from apm2go reading /proc directly, not from OBI: measured
# directly, none of OBI's own metric features produced runtime figures for a
# non-Java process, only HTTP request counters.
for _ in $(seq 1 10); do
    has_proc_metrics="$(api 'metrics?from=-5m&service=go-caller' | python3 -c "
import json, sys
names = {m['name'] for m in json.load(sys.stdin)['metrics']}
print(1 if 'process.memory.usage' in names else 0)
" 2>/dev/null || echo 0)"
    [ "${has_proc_metrics}" = "1" ] && break
    docker exec "${CONTAINER}" curl -sf http://127.0.0.1:8096/ >/dev/null 2>&1 || true
    sleep 3
done
[ "${has_proc_metrics}" = "1" ] || fail "go-caller reported no process.memory.usage"
echo "    process-level memory reported for a non-Java service"

echo
echo "PASS — installed from RPM, Node.js/Python/Go services discovered and"
echo "       instrumented via eBPF with no restart, each joining the Java"
echo "       chain in one trace; no Java service double-instrumented; the"
echo "       eBPF subprocess dying neither breaks Java nor stays down."
