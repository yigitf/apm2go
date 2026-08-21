#!/usr/bin/env bash
#
# End-to-end acceptance test.
#
# Installs apm2go from its RPM on a clean Rocky Linux image, where a four-node
# service chain is already running and serving traffic, and verifies the whole
# chain of claims: those JVMs are discovered, instrumented without restarting
# them, and a single request through the chain produces one trace spanning
# every service it touched.
#
# The workload contains no tracing code. Everything asserted below is something
# apm2go produced by attaching to processes that were already running.
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

CONTAINER="apm2go-e2e-$$"
cleanup() { docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> Compiling the chain workload"
./build/chain/build-app.sh >/dev/null

echo "==> Building acceptance image from ${RPM_FILE}"
docker build -q -f build/Dockerfile.rpmtest --build-arg "RPM_FILE=${RPM_FILE}" \
    -t apm2go-rpmtest:latest . >/dev/null

echo "==> Starting the chain, then apm2go against it"
docker run -d --name "${CONTAINER}" -p 18080:8080 apm2go-rpmtest:latest bash -c '
    # Shorten the start-up grace period so the test does not wait out a
    # production default; nothing else about the shipped config is changed.
    sed -i "s|min_uptime: 10s|min_uptime: 1s|" /etc/apm2go/config.yaml
    exec apm2go run --config /etc/apm2go/config.yaml
' >/dev/null

api() { curl -sf "http://127.0.0.1:18080/api/v1/$1"; }
fail() { echo "FAIL: $1"; docker logs "${CONTAINER}" 2>&1 | tail -30; exit 1; }

# The API returns single-line JSON, so counting with `grep -c` would report the
# number of matching lines — always 1 — rather than the number of matches.
count_jvms_in_state() {
    api jvms | python3 -c "
import json, sys
state = sys.argv[1]
print(sum(1 for entry in json.load(sys.stdin) if entry['state'] == state))
" "$1" 2>/dev/null || echo 0
}

count_traces() {
    api "traces?from=-10m&limit=${1:-5}" | python3 -c "
import json, sys
print(len(json.load(sys.stdin)['traces']))
" 2>/dev/null || echo 0
}

# The pids the chain started with, captured before apm2go has done anything.
# Written to a file rather than an associative array so this runs under the
# bash 3.2 that macOS still ships.
sleep 8
START_PIDS_FILE="$(mktemp)"
trap 'cleanup; rm -f "${START_PIDS_FILE}"' EXIT

docker exec "${CONTAINER}" bash -c '
    for dir in /proc/[0-9]*; do
        cmdline="$(tr "\0" " " < "${dir}/cmdline" 2>/dev/null)" || continue
        case "${cmdline}" in
            *ChainNode*)
                for svc in gateway orders inventory payments; do
                    case "${cmdline}" in
                        *"${svc}-service"*) echo "${svc} $(basename "${dir}")" ;;
                    esac
                done
                ;;
        esac
    done
' > "${START_PIDS_FILE}"

start_pid_of() { awk -v s="$1" '$1 == s { print $2; exit }' "${START_PIDS_FILE}"; }

echo "==> Waiting for the API"
for _ in $(seq 1 60); do
    if api health >/dev/null 2>&1; then break; fi
    sleep 1
done
api health >/dev/null || fail "the API never became reachable"

echo "==> Checking that all four running JVMs were discovered"
for _ in $(seq 1 30); do
    [ "$(api jvms | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)" -ge 4 ] && break
    sleep 1
done
for svc in gateway-service orders-service inventory-service payments-service; do
    api jvms | grep -q "\"service_name\":\"${svc}\"" || fail "${svc} was not discovered"
done

echo "==> Checking that they were instrumented without a restart"
for _ in $(seq 1 60); do
    [ "$(count_jvms_in_state attached)" -ge 4 ] && break
    sleep 1
done
attached="$(count_jvms_in_state attached)"
[ "${attached}" -ge 4 ] || fail "only ${attached} of 4 JVMs reached the attached state"
echo "    all 4 attached"

echo "==> Checking that traces arrived and were stored"
for _ in $(seq 1 60); do
    [ "$(count_traces 5)" -gt 0 ] && break
    sleep 2
done
[ "$(count_traces 5)" -gt 0 ] || fail "no traces were stored"

echo "==> Checking that one request produces one trace across all four services"
# This is the assertion the whole design is for: context propagating from the
# gateway through orders into inventory, and separately into payments, all
# stitched into a single trace by instrumentation nobody wrote into the app.
for _ in $(seq 1 30); do
    multi="$(api 'traces?from=-10m&limit=50' | python3 -c "
import json, sys
traces = json.load(sys.stdin)['traces']
print(sum(1 for t in traces if t['service_count'] == 4))
" 2>/dev/null || echo 0)"
    [ "${multi}" -gt 0 ] && break
    sleep 2
done
[ "${multi}" -gt 0 ] || fail "no trace spanned all four services; cross-service context propagation is broken"

echo "==> Checking the shape of that trace"
trace_id="$(api 'traces?from=-10m&limit=50' | python3 -c "
import json, sys
traces = json.load(sys.stdin)['traces']
multi = [t for t in traces if t['service_count'] == 4]
print(multi[0]['trace_id'] if multi else '')
")"
[ -n "${trace_id}" ] || fail "could not read a multi-service trace id"

api "traces/${trace_id}" | python3 -c "
import json, sys
trace = json.load(sys.stdin)
services = set(trace['services'])
expected = {'gateway-service', 'orders-service', 'inventory-service', 'payments-service'}
missing = expected - services
if missing:
    raise SystemExit('missing services in trace: %s' % sorted(missing))

kinds = [s['kind'] for s in trace['spans']]
# Kind 2 is SERVER: one per service that handled the request. Without these the
# trace would be a set of disconnected client calls rather than a waterfall.
if kinds.count(2) < 4:
    raise SystemExit('expected a server span per service, found %d' % kinds.count(2))
# Kind 3 is CLIENT: the outgoing HTTP calls plus each node's database query.
if kinds.count(3) < 4:
    raise SystemExit('expected client spans for downstream and database calls, found %d' % kinds.count(3))
if not any(s.get('db_statement') for s in trace['spans']):
    raise SystemExit('no database span was captured')

roots = [s for s in trace['spans'] if not s.get('parent_span_id')]
if len(roots) != 1:
    raise SystemExit('expected exactly one root span, found %d' % len(roots))

print('    %d spans, %d services, %d server spans, single root'
      % (len(trace['spans']), len(services), kinds.count(2)))
" || fail "the trace did not have the expected multi-service shape"

echo "==> Checking that the host is measured"
# apm2go measures the machine it runs on, so this needs nothing instrumented
# and is what a metrics view shows before any JVM is attached.
for _ in $(seq 1 30); do
    host_metrics="$(api 'metrics?from=-10m' | python3 -c "
import json, sys
print(len(json.load(sys.stdin)['metrics']))" 2>/dev/null || echo 0)"
    [ "${host_metrics}" -gt 0 ] && break
    sleep 3
done
[ "${host_metrics}" -gt 0 ] || fail "no host metrics were collected"
api 'metrics?from=-10m' | python3 -c "
import json, sys
names = {m['name'] for m in json.load(sys.stdin)['metrics']}
required = {'system.cpu.utilization', 'system.memory.utilization'}
missing = required - names
if missing:
    raise SystemExit('missing host metrics: %s' % sorted(missing))
print('    %d host instruments, including CPU and memory' % len(names))
" || fail "the host metrics are incomplete"

echo "==> Checking that instrumented JVMs report their runtime state"
# The agent exports on an interval, so this is the one check that has to wait
# for a clock rather than for work to happen.
for _ in $(seq 1 40); do
    jvm_metrics="$(api 'metrics?from=-10m&service=gateway-service' | python3 -c "
import json, sys
print(len(json.load(sys.stdin)['metrics']))" 2>/dev/null || echo 0)"
    [ "${jvm_metrics}" -gt 0 ] && break
    sleep 3
done
[ "${jvm_metrics}" -gt 0 ] || fail "the instrumented JVM reported no runtime metrics"

# Heap is reported per memory pool, so several series share the instrument —
# a single merged series would mean the pool attribute had been lost.
api 'metrics/query?from=-10m&service=gateway-service&name=jvm.memory.used' | python3 -c "
import json, sys
series = json.load(sys.stdin)['series']
if len(series) < 2:
    raise SystemExit('expected heap broken down by pool, got %d series' % len(series))
if not any(s['points'] for s in series):
    raise SystemExit('heap series carry no points')
print('    heap reported across %d memory pools' % len(series))
" || fail "JVM heap metrics have the wrong shape"

echo "==> Checking that a thread dump can be taken from a live JVM"
# The payments node deadlocked two of its threads on start-up and has been
# serving requests normally ever since. Nothing observed so far — not a trace,
# not a metric — shows that fault; only looking inside the running JVM does.
payments_pid="$(start_pid_of payments)"
[ -n "${payments_pid}" ] || fail "could not determine the payments pid"

dump="$(curl -sf -X POST "http://127.0.0.1:18080/api/v1/jvms/${payments_pid}/diagnostics/thread_dump")" \
    || fail "the thread dump request failed"

echo "${dump}" | python3 -c "
import json, sys
result = json.load(sys.stdin)
threads = result['summary']['threads']
if len(threads['threads']) < 5:
    raise SystemExit('parsed only %d threads' % len(threads['threads']))
if not result['stored']:
    raise SystemExit('the dump was not stored, so it cannot be compared later')

deadlocks = threads.get('deadlocks') or []
if not deadlocks:
    raise SystemExit('no deadlock found in a JVM that is deliberately deadlocked')

names = {n for d in deadlocks for n in d['threads']}
expected = {'chain-deadlock-a', 'chain-deadlock-b'}
if not expected <= names:
    raise SystemExit('deadlock names the wrong threads: %s' % sorted(names))

print('    %d threads parsed, deadlock found between %s (source: %s), JVM paused %d ms'
      % (len(threads['threads']), ' and '.join(sorted(expected)),
         deadlocks[0]['source'], result['duration_ms']))
" || fail "the thread dump did not detect the deliberate deadlock"

echo "==> Checking that a heap histogram can be taken and stored"
curl -sf -X POST "http://127.0.0.1:18080/api/v1/jvms/${payments_pid}/diagnostics/class_histogram" \
    | python3 -c "
import json, sys
result = json.load(sys.stdin)
histogram = result['summary']['histogram']
if histogram['class_count'] < 100:
    raise SystemExit('a running JVM reported only %d classes' % histogram['class_count'])
if histogram['total_bytes'] <= 0:
    raise SystemExit('histogram reports no live bytes')
names = {c['name'] for c in histogram['classes']}
# Every JVM has byte arrays and strings alive; their absence means the table
# was misparsed rather than that the heap is unusual.
if not ({'[B', 'java.lang.String'} & names):
    raise SystemExit('histogram rows did not parse: %s' % sorted(names)[:5])
print('    %d classes, %.1f MB live' % (histogram['class_count'], histogram['total_bytes'] / 1048576))
" || fail "the heap histogram was not collected or did not parse"

echo "==> Checking that the dumps are listed in the JVM's history"
api "jvms/${payments_pid}/diagnostics" | python3 -c "
import json, sys
body = json.load(sys.stdin)
kinds = {d['kind'] for d in body['diagnostics']}
missing = {'thread_dump', 'class_histogram'} - kinds
if missing:
    raise SystemExit('missing from history: %s' % sorted(missing))
for d in body['diagnostics']:
    if d.get('raw'):
        raise SystemExit('the history carried a full dump body')
    if not d.get('headline'):
        raise SystemExit('a stored dump has no headline for the list to show')
# The one command apm2go refuses to run must still be reachable as text.
if 'GC.heap_dump' not in body['heap_dump']['command']:
    raise SystemExit('the heap dump command is not offered')
print('    %d stored dumps, bodies excluded from the list' % len(body['diagnostics']))
" || fail "the diagnostics history is wrong"

echo "==> Checking that a stored dump keeps the JVM's verbatim output"
dump_id="$(api "jvms/${payments_pid}/diagnostics?kind=thread_dump" | python3 -c "
import json, sys
print(json.load(sys.stdin)['diagnostics'][0]['id'])")"
curl -sf "http://127.0.0.1:18080/api/v1/diagnostics/${dump_id}/raw" \
    | grep -q "Full thread dump" \
    || fail "the raw dump was not stored verbatim"

echo "==> Checking that taking dumps did not restart or break the target"
# A diagnostic pauses the JVM at a safepoint. If that pause were mishandled the
# process would die or stop serving, which is worse than not looking at all.
docker exec "${CONTAINER}" test -d "/proc/${payments_pid}" \
    || fail "payments pid ${payments_pid} is gone: a diagnostic killed it"
docker exec "${CONTAINER}" curl -sf "http://127.0.0.1:8084/health" >/dev/null \
    || fail "payments stopped serving after its threads were dumped"

echo "==> Checking that the web UI is served"
curl -sf "http://127.0.0.1:18080/" | grep -q "<title>apm2go</title>" || fail "the web UI was not served"

echo "==> Checking that no JVM was restarted"
# Instrumenting a live process is the entire premise, so a changed pid would
# invalidate every assertion above even though they all passed.
summary=""
for svc in gateway orders inventory payments; do
    started="$(start_pid_of "${svc}")"
    [ -n "${started}" ] || fail "could not determine the start-up pid for ${svc}"
    docker exec "${CONTAINER}" test -d "/proc/${started}" \
        || fail "${svc} pid ${started} is gone: the process restarted"
    summary="${summary}${svc}=${started} "
done
echo "    ${summary}— all original pids"

echo
echo "PASS — installed from RPM, four already-running JVMs discovered and instrumented"
echo "       without restart, one request traced across all four services, and a"
echo "       deliberate deadlock found by dumping a live JVM's threads."
