#!/bin/sh
# hello.version + client_outdated (csharp) against the shared wssserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
. "$REPO/sdks/testtools/hv-common.sh"
cd "$REPO/sdks/csharp"
echo "== build =="
dotnet build tests/hello_version -v q 2>&1 | grep -E 'error|Build succeeded' | head -2
hv_build_fixture
run_cs() { ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" timeout 60 dotnet run --no-build --project tests/hello_version; }
echo "== case 1: hello.version =="
rm -f /tmp/hv-cs.hello
hv_run_case /tmp/hv-cs.hello -
run_cs > /tmp/hv-cs.out 2>&1 || true
hv_stop_fixture
# structural check: the frame must be valid JSON (a truncated hello once
# slipped past a substring grep) with version == VERSION and derived client.
python3 - <<'PY'
import json
h = json.loads(open('/tmp/hv-cs.hello').read().strip().splitlines()[-1])
assert h.get("type") == "hello" and h.get("proto") == 1, h
assert h.get("version") == "1.7.39", h
assert h.get("client") == "etunnel-csharp/1.7.39", h
PY
echo "hello: $(cat /tmp/hv-cs.hello)"
echo "== case 2: client_outdated =="
hv_run_case - client_outdated
rc=0
run_cs > /tmp/hv-cs.out 2>&1 || rc=$?
hv_stop_fixture
[ "$rc" -ne 0 ] || { echo "FAIL: exit 0 on client_outdated"; cat /tmp/hv-cs.out; exit 1; }
grep -q 'too old' /tmp/hv-cs.out && grep -q 'https://github.com/topcheer/etunnel' /tmp/hv-cs.out || { echo "FAIL: message not surfaced"; cat /tmp/hv-cs.out; exit 1; }
echo "PASS: csharp hello.version + client_outdated (exit $rc)"
