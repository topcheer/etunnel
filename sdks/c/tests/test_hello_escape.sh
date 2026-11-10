#!/bin/sh
# #48 regression ③: a token containing quotes/backslashes must arrive intact
# in hello. Pre-fix, the raw interpolation produced invalid JSON (#48 MED).
set -eu
cd "$(dirname "$0")/.."

echo "== build =="
make -s etunnel
echo "build ok"

echo "== fixture: hellocheck (plain ws) =="
ADDR_FILE=$(mktemp)
TOKEN='tok"with"quotes\and\backslash'
( ET_TEST_TOKEN="$TOKEN" go run ./testtools/wssserver -mode hellocheck -plain >"$ADDR_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$ADDR_FILE" ] && break; sleep 1; done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture did not start"; exit 1; }
ADDR=$(cat "$ADDR_FILE")
echo "fixture on $ADDR"

TOKEN='tok"with"quotes\and\backslash'
OUT=$(ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    ET_TEST_TOKEN_DUMMY=1 ETUNNEL_TOKEN="$TOKEN" timeout 8 ./etunnel 9 2>&1 || true)
echo "fixture says:"; grep -E 'HELLO_(OK|BAD)' "$ADDR_FILE" | sed 's/^/    /' || true

if grep -q 'HELLO_OK' "$ADDR_FILE"; then
    echo "PASS: hello_escape"
    exit 0
fi
echo "FAIL: hello did not carry the token intact ($TOKEN)"
exit 1
