#!/bin/sh
# hello.version + client_outdated (java) against the shared wssserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
. "$REPO/sdks/testtools/hv-common.sh"
cd "$REPO/sdks/java"
echo "== build =="
rm -rf /tmp/hv-java && mkdir -p /tmp/hv-java && javac -d /tmp/hv-java src/main/java/dev/ggcode/Etunnel.java
hv_build_fixture
echo "== case 1: hello.version =="
rm -f /tmp/hv-java.hello
hv_run_case /tmp/hv-java.hello -
ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" ETUNNEL_RECONNECT=0 timeout 25 java -cp /tmp/hv-java dev.ggcode.Etunnel 3000 > /tmp/hv-java.out 2>&1 || true
hv_stop_fixture
# structural check: the frame must be valid JSON (a truncated hello once
# slipped past a substring grep) with version == VERSION and derived client.
python3 - <<'PY'
import json
h = json.loads(open('/tmp/hv-java.hello').read().strip().splitlines()[-1])
assert h.get("type") == "hello" and h.get("proto") == 1, h
assert h.get("version") == "1.7.39", h
assert h.get("client") == "etunnel-java/1.7.39", h
PY
echo "hello: $(cat /tmp/hv-java.hello)"
echo "== case 2: client_outdated =="
hv_run_case - client_outdated
rc=0
ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" timeout 25 java -cp /tmp/hv-java dev.ggcode.Etunnel 3000 > /tmp/hv-java.out 2>&1 || rc=$?
hv_stop_fixture
[ "$rc" -ne 0 ] || { echo "FAIL: exit 0 on client_outdated"; cat /tmp/hv-java.out; exit 1; }
grep -q 'too old' /tmp/hv-java.out && grep -q 'https://github.com/topcheer/etunnel' /tmp/hv-java.out || { echo "FAIL: message not surfaced"; cat /tmp/hv-java.out; exit 1; }
echo "PASS: java hello.version + client_outdated (exit $rc)"
