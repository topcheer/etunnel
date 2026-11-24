#!/bin/sh
# Regression: the C client must not send an Origin header. etunneld >= v1.7.29
# (server/session.go handleTunnelWS, #321) rejects any Origin with 403 - the
# client used to set cc.origin = host and could no longer connect to real
# servers, while this fixture (which accepted everything) let the tests pass.
# The fixture now mirrors that check; the client must still get its welcome.
set -eu
cd "$(dirname "$0")/.."

echo "== build =="
make 2>&1 | tail -1
go build -o /tmp/wss_noorigin ./testtools/wssserver
echo "build ok"

echo "== fixture (mirrors etunneld: Origin -> 403) =="
ADDR_FILE=$(mktemp)
/tmp/wss_noorigin -listen 127.0.0.1:0 -plain > "$ADDR_FILE" &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$ADDR_FILE" ] && break; sleep 0.2; done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture did not start"; exit 1; }
ADDR=$(head -1 "$ADDR_FILE")

echo "== client =="
rc=0
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 ETUNNEL_RECONNECT=0 \
    timeout 5 ./etunnel 3000 > /tmp/c_noorigin.out 2> /tmp/c_noorigin.err || rc=$?

if grep -q '^TUNNEL_URL:' /tmp/c_noorigin.out; then
    echo "PASS: $(grep '^TUNNEL_URL:' /tmp/c_noorigin.out)"
    exit 0
fi
echo "FAIL: client never got its welcome (rc=$rc)"
sed 's/^/    /' /tmp/c_noorigin.err | head -3
grep ORIGIN_REJECTED "$ADDR_FILE" | sed 's/^/    fixture: /' || true
exit 1
