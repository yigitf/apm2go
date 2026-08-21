#!/usr/bin/env bash
#
# Container acceptance test.
#
# The workload runs in its own container — its own pid, mount and network
# namespaces — and apm2go runs separately, in the host's namespaces, from the
# image it ships as. Nothing is shared between them except the kernel.
#
# This is the case that did not work before M5: attaching across the boundary
# always did, but a containerized JVM told to export to 127.0.0.1 was exporting
# into its own loopback, where nothing was listening. What is asserted here is
# the whole path — discovery, attach, gateway resolution, ingest binding, token
# authentication and storage — with the container boundary in the middle of it.
set -euo pipefail

cd "$(dirname "$0")/.."

CHAIN_IMAGE="${CHAIN_IMAGE:-apm2go-rpmtest:latest}"
APM2GO_IMAGE="${APM2GO_IMAGE:-apm2go:latest}"

CHAIN="apm2go-e2e-chain-$$"
AGENT="apm2go-e2e-agent-$$"
cleanup() { docker rm -f "${CHAIN}" "${AGENT}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

fail() {
    echo "FAIL: $1"
    echo "--- apm2go logs ---"; docker logs "${AGENT}" 2>&1 | tail -25
    exit 1
}

# apm2go is queried from inside its own container: with --network=host it lives
# in the Linux VM's network namespace, which on macOS the host cannot reach.
api() { docker exec "${AGENT}" curl -sf "http://127.0.0.1:8080/api/v1/$1"; }

echo "==> Starting the workload in its own container"
docker run -d --name "${CHAIN}" "${CHAIN_IMAGE}" sleep infinity >/dev/null
sleep 12
docker exec "${CHAIN}" bash -c 'grep -lh listening /var/log/chain/*.log >/dev/null' \
    || fail "the chain never started inside its container"

# The pids the workload started with, read from its own namespace.
CHAIN_PIDS="$(docker exec "${CHAIN}" bash -c 'pgrep -x java | sort -n | tr "\n" " "')"
[ -n "${CHAIN_PIDS}" ] || fail "no chain processes found in the workload container"
echo "    workload pids (in its own namespace): ${CHAIN_PIDS}"

echo "==> Starting apm2go from its image, in the host's namespaces"
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
  require_token: true
YAML
exec apm2go --config /tmp/cfg.yaml run' >/dev/null

echo "==> Waiting for the API"
for _ in $(seq 1 60); do
    api health >/dev/null 2>&1 && break
    sleep 1
done
api health >/dev/null 2>&1 || fail "the API never became reachable"

count_state() {
    api jvms | python3 -c "
import json, sys
print(sum(1 for e in json.load(sys.stdin) if e['state'] == sys.argv[1]))
" "$1" 2>/dev/null || echo 0
}

echo "==> Checking that the containerized JVMs were discovered"
for _ in $(seq 1 40); do
    [ "$(api jvms | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)" -ge 4 ] && break
    sleep 1
done

echo "==> Checking that they were classified as containerized and unreachable by loopback"
api jvms | python3 -c "
import json, sys
entries = json.load(sys.stdin)
containerized = [e for e in entries if e['jvm']['in_container']]
if len(containerized) < 4:
    raise SystemExit('expected 4 containerized JVMs, found %d' % len(containerized))

# Each must be seen as outside apm2go's network, with a gateway resolved for it.
for e in containerized:
    jvm = e['jvm']
    if jvm['shares_our_network']:
        raise SystemExit('%s was reported as sharing apm2go network namespace' % jvm['service_name'])
    if not jvm.get('gateway'):
        raise SystemExit('%s has no resolved gateway' % jvm['service_name'])
print('    all 4 containerized, gateway resolved:', containerized[0]['jvm']['gateway'])
" || fail "namespace classification is wrong"

echo "==> Checking that they were instrumented across the container boundary"
for _ in $(seq 1 60); do
    [ "$(count_state attached)" -ge 4 ] && break
    sleep 1
done
[ "$(count_state attached)" -ge 4 ] || fail "only $(count_state attached) of 4 were attached"
echo "    all 4 attached"

echo "==> Checking that ingest was extended to the container network"
api self | python3 -c "
import json, sys
addresses = json.load(sys.stdin)['receiver'].get('listen_addresses') or []
external = [a for a in addresses if not a.startswith('127.')]
if not external:
    raise SystemExit('ingest is still loopback-only: %s' % addresses)
print('    listening on', ', '.join(addresses))
" || fail "the receiver never bound the container gateway"

echo "==> Checking that traces crossed the boundary and were stored"
for _ in $(seq 1 60); do
    n="$(api 'traces?from=-10m&limit=5' | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["traces"]))' 2>/dev/null || echo 0)"
    [ "${n}" -gt 0 ] && break
    sleep 2
done
[ "${n}" -gt 0 ] || fail "no traces arrived from the containerized workload"

echo "==> Checking that one request is one trace across all four services"
for _ in $(seq 1 30); do
    multi="$(api 'traces?from=-10m&limit=50' | python3 -c "
import json, sys
print(sum(1 for t in json.load(sys.stdin)['traces'] if t['service_count'] == 4))" 2>/dev/null || echo 0)"
    [ "${multi}" -gt 0 ] && break
    sleep 2
done
[ "${multi}" -gt 0 ] || fail "no trace spanned all four services"

trace_id="$(api 'traces?from=-10m&limit=50' | python3 -c "
import json, sys
multi = [t for t in json.load(sys.stdin)['traces'] if t['service_count'] == 4]
print(multi[0]['trace_id'] if multi else '')")"
api "traces/${trace_id}" | python3 -c "
import json, sys
trace = json.load(sys.stdin)
kinds = [s['kind'] for s in trace['spans']]
if kinds.count(2) < 4:
    raise SystemExit('expected a server span per service, found %d' % kinds.count(2))
roots = [s for s in trace['spans'] if not s.get('parent_span_id')]
if len(roots) != 1:
    raise SystemExit('expected one root span, found %d' % len(roots))
print('    %d spans, %d services, %d server spans, single root'
      % (len(trace['spans']), len(trace['services']), kinds.count(2)))
" || fail "the cross-container trace has the wrong shape"

echo "==> Checking that unauthenticated telemetry is refused"
# Anything else on that bridge can reach ingest; only what apm2go instrumented
# carries a token, and only that should be accepted.
gateway="$(api jvms | python3 -c "
import json, sys
print(json.load(sys.stdin)[0]['jvm'].get('gateway') or '')")"
[ -n "${gateway}" ] || fail "no gateway to test authentication against"

status="$(docker exec "${CHAIN}" curl -s -o /dev/null -w '%{http_code}' \
    -X POST -H 'Content-Type: application/x-protobuf' --data-binary '' \
    "http://${gateway}:4318/v1/traces" || echo 000)"
[ "${status}" = "401" ] || fail "an export with no token returned ${status}, want 401"
api self | python3 -c "
import json, sys
rejected = json.load(sys.stdin)['receiver']['unauthenticated']
if rejected < 1:
    raise SystemExit('the rejection was not counted')
print('    unauthenticated export refused and counted (%d)' % rejected)
" || fail "the rejection was not recorded"

echo "==> Checking that no workload process was restarted"
NOW_PIDS="$(docker exec "${CHAIN}" bash -c 'pgrep -x java | sort -n | tr "\n" " "')"
[ "${CHAIN_PIDS}" = "${NOW_PIDS}" ] \
    || fail "workload pids changed: ${CHAIN_PIDS} -> ${NOW_PIDS}"
echo "    ${NOW_PIDS}— unchanged"

echo
echo "PASS — a workload in its own container, instrumented from apm2go's image"
echo "       running in the host's namespaces, with no restart: gateway resolved,"
echo "       ingest extended to it, tokens enforced, one trace across four services."
