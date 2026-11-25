#!/bin/sh
# hello.version + client_outdated (kotlin) against the shared wssserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
. "$REPO/sdks/testtools/hv-common.sh"
cd "$REPO/sdks/kotlin"
echo "== build =="
rm -rf /tmp/hv-kt.jar && kotlinc Etunnel.kt -include-runtime -d /tmp/hv-kt.jar 2>&1 | grep -E 'error:' || true
GC="$HOME/.gradle/caches/modules-2/files-2.1"
OKHTTP=$(ls "$GC"/com.squareup.okhttp3/okhttp/4.12.0/*/okhttp-4.12.0.jar 2>/dev/null | head -1)
OKIO=$(ls "$GC"/com.squareup.okio/okio-jvm/3.9.1/*/okio-jvm-3.9.1.jar 2>/dev/null | head -1)
CP="/tmp/hv-kt.jar:$OKHTTP:$OKIO"
hv_build_fixture
run_kt() { ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" ETUNNEL_RECONNECT=0 timeout 25 java -cp "$CP" dev.ggcode.etunnel.EtunnelKt 3000; }
echo "== case 1: hello.version =="
rm -f /tmp/hv-kt.hello
hv_run_case /tmp/hv-kt.hello -
run_kt > /tmp/hv-kt.out 2>&1 || true
hv_stop_fixture
# structural check: the frame must be valid JSON (a truncated hello once
# slipped past a substring grep) with version == VERSION and derived client.
python3 - <<'PY'
import json
h = json.loads(open('/tmp/hv-kt.hello').read().strip().splitlines()[-1])
assert h.get("type") == "hello" and h.get("proto") == 1, h
assert h.get("version") == "1.7.39", h
assert h.get("client") == "etunnel-kotlin/1.7.39", h
PY
echo "hello: $(cat /tmp/hv-kt.hello)"
echo "== case 2: client_outdated =="
hv_run_case - client_outdated
rc=0
run_kt > /tmp/hv-kt.out 2>&1 || rc=$?
hv_stop_fixture
[ "$rc" -ne 0 ] || { echo "FAIL: exit 0 on client_outdated"; cat /tmp/hv-kt.out; exit 1; }
grep -q 'too old' /tmp/hv-kt.out && grep -q 'https://github.com/topcheer/etunnel' /tmp/hv-kt.out || { echo "FAIL: message not surfaced"; cat /tmp/hv-kt.out; exit 1; }
echo "PASS: kotlin hello.version + client_outdated (exit $rc)"
