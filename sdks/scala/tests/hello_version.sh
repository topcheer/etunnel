#!/bin/sh
# hello.version + client_outdated (scala) against the shared wssserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
. "$REPO/sdks/testtools/hv-common.sh"
cd "$REPO/sdks/scala"
echo "== build =="
rm -rf /tmp/hv-sc && mkdir -p /tmp/hv-sc && scalac -d /tmp/hv-sc Etunnel.scala 2>&1 | grep -E 'Error' && exit 1 || true
hv_build_fixture
SCALA_LIB=$(ls /opt/homebrew/Cellar/scala/*/libexec/maven2/org/scala-lang/scala3-library_3/*/scala3-library_3-*.jar 2>/dev/null | grep -v sources | head -1)
SCALA_STDLIB=$(ls /opt/homebrew/Cellar/scala/*/libexec/maven2/org/scala-lang/scala-library/*/scala-library-*.jar 2>/dev/null | grep -v sources | head -1)
# Scala 3 top-level `fun main` compiles to dev.ggcode.etunnel.Main (the
# scala runner cannot take a class name) - run it on the JVM directly.
run_sc() { ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" ETUNNEL_RECONNECT=0 timeout 40 java -cp "/tmp/hv-sc:$SCALA_LIB:$SCALA_STDLIB" dev.ggcode.etunnel.Main 3000; }
echo "== case 1: hello.version =="
rm -f /tmp/hv-sc.hello
hv_run_case /tmp/hv-sc.hello -
run_sc > /tmp/hv-sc.out 2>&1 || true
hv_stop_fixture
# structural check: the frame must be valid JSON (a truncated hello once
# slipped past a substring grep) with version == VERSION and derived client.
python3 - <<'PY'
import json
h = json.loads(open('/tmp/hv-sc.hello').read().strip().splitlines()[-1])
assert h.get("type") == "hello" and h.get("proto") == 1, h
assert h.get("version") == "1.7.39", h
assert h.get("client") == "etunnel-scala/1.7.39", h
PY
echo "hello: $(cat /tmp/hv-sc.hello)"
echo "== case 2: client_outdated =="
hv_run_case - client_outdated
rc=0
run_sc > /tmp/hv-sc.out 2>&1 || rc=$?
hv_stop_fixture
[ "$rc" -ne 0 ] || { echo "FAIL: exit 0 on client_outdated"; cat /tmp/hv-sc.out; exit 1; }
grep -q 'too old' /tmp/hv-sc.out && grep -q 'https://github.com/topcheer/etunnel' /tmp/hv-sc.out || { echo "FAIL: message not surfaced"; cat /tmp/hv-sc.out; exit 1; }
echo "PASS: scala hello.version + client_outdated (exit $rc)"
