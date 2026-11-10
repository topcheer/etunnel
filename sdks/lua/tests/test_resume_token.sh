#!/bin/sh
# Issue #52 review must-fix regression: the resume token extracted from a
# welcome must survive into the supervisor's reconnect hello. A block-local
# `local resume_token` (the reviewed bug) died at the if-end, so the replayed
# hello carried no resume and the fixture printed HELLO_RESUME_MISSING.
set -eu
cd "$(dirname "$0")/.."

echo "== fixture =="
OUT_FILE=$(mktemp)
( go run ./testtools/wssserver >"$OUT_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$OUT_FILE" ] && break; sleep 1; done
[ -s "$OUT_FILE" ] || { echo "FAIL: fixture did not start"; exit 1; }
ADDR=$(head -1 "$OUT_FILE")
echo "fixture on $ADDR"

echo "== run client (kill after the reconnect assertion window) =="
( ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=9 timeout 12 lua etunnel.lua 9 2>&1 || true ) &
CLIENT=$!
sleep 8
kill $CLIENT 2>/dev/null || true

echo "== verdict =="
if grep -q 'HELLO_RESUME_OK' "$OUT_FILE"; then
    echo "PASS: resume_token survives welcome -> supervisor -> hello"
    exit 0
fi
if grep -q 'HELLO_RESUME_MISSING' "$OUT_FILE"; then
    echo "FAIL: reconnect hello did not carry the resume token (scope regression)"
    exit 1
fi
echo "FAIL: fixture never saw the reconnect ($(cat "$OUT_FILE" | tr '\n' ' '))"
exit 1
