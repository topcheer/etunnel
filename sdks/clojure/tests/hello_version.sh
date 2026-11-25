#!/bin/sh
# hello.version + client_outdated (clojure) against the shared wssserver fixture.
# Covers the fixer's finding: clojure had NO error-frame branch (server text
# was lost to a 15s timeout) - now connect! throws ex-info with code+message
# and the CLI exits non-zero on client_outdated.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
. "$REPO/sdks/testtools/hv-common.sh"
cd "$REPO/sdks/clojure"
hv_build_fixture
run_clj() { ETUNNEL_SERVER="ws://$HV_ADDR/tunnel" ETUNNEL_RECONNECT=0 timeout 60 clojure -M -e "(load-file \"etunnel.clj\") (etunnel/-main \"3000\")"; }
echo "== case 1: hello.version =="
rm -f /tmp/hv-clj.hello
hv_run_case /tmp/hv-clj.hello -
run_clj > /tmp/hv-clj.out 2>&1 || true
hv_stop_fixture
# structural check: the frame must be valid JSON (a truncated hello once
# slipped past a substring grep) with version == VERSION and derived client.
python3 - <<'PY'
import json
h = json.loads(open('/tmp/hv-clj.hello').read().strip().splitlines()[-1])
assert h.get("type") == "hello" and h.get("proto") == 1, h
assert h.get("version") == "1.7.39", h
assert h.get("client") == "etunnel-clojure/1.7.39", h
PY
echo "hello: $(cat /tmp/hv-clj.hello)"
echo "== case 2: client_outdated =="
hv_run_case - client_outdated
rc=0
run_clj > /tmp/hv-clj.out 2>&1 || rc=$?
hv_stop_fixture
[ "$rc" -ne 0 ] || { echo "FAIL: exit 0 on client_outdated"; cat /tmp/hv-clj.out; exit 1; }
grep -q 'too old' /tmp/hv-clj.out && grep -q 'https://github.com/topcheer/etunnel' /tmp/hv-clj.out || { echo "FAIL: message not surfaced"; cat /tmp/hv-clj.out; exit 1; }
echo "PASS: clojure hello.version + client_outdated (exit $rc)"
