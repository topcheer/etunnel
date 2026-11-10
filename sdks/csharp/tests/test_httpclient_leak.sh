#!/bin/sh
# #54 regression ③: every reconnect used to mint a new HttpClient (infinite
# timeout, keep-alive pool) that was never disposed. Watch the process's open
# file descriptors across many forced reconnects: disposing on Loop exit keeps
# the count flat, a leak grows it with every cycle.
set -eu
cd "$(dirname "$0")/.."

go build -o /tmp/wssc-cs ./testtools/wssserver
ADDR_FILE=$(mktemp)
( /tmp/wssc-cs -mode drama -plain >"$ADDR_FILE" 2>/dev/null ) &
FX=$!
trap 'kill $FX 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
ADDR=$(head -1 "$ADDR_FILE")

ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=39503 \
    dotnet run --no-build > /tmp/cs-fd.log 2>&1 &
CLI=$!
trap 'kill $CLI $FX 2>/dev/null || true' EXIT

sleep 4
FDS_A=$(lsof -p $CLI 2>/dev/null | wc -l | tr -d ' ')
sleep 12
FDS_B=$(lsof -p $CLI 2>/dev/null | wc -l | tr -d ' ')
CYCLES=$(grep -c TUNNEL_URL /tmp/cs-fd.log || true)
echo "fd table: $FDS_A -> $FDS_B after $CYCLES reconnect cycles"

[ "$CYCLES" -ge 8 ] || { echo "FAIL: not enough reconnect cycles ($CYCLES)"; exit 1; }
DELTA=$(( FDS_B - FDS_A ))
[ "$DELTA" -le 10 ] || { echo "FAIL: fd table grew by $DELTA across $CYCLES reconnects (leak)"; exit 1; }
echo "PASS: httpclient_no_leak"
