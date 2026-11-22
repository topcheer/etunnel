#!/bin/sh
# #338 batch 2: a welcome advertising proto=2 must fail the connect with a
# proto_version error (missing proto = v1 tolerance).
set -eu
cd "$(dirname "$0")/.."
LUA=${LUA:-lua}
echo "== fixture (ETUNNEL_FIXTURE_PROTO=2) =="
go build -o /tmp/wss338lua ./testtools/wssserver
ETUNNEL_FIXTURE_PROTO=2 /tmp/wss338lua -listen 127.0.0.1:0 -plain > /tmp/wss338lua.addr &
FPID=$!
sleep 0.5
ADDR=$(head -1 /tmp/wss338lua.addr)
echo "== client =="
rc=0
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 timeout 10 $LUA etunnel.lua 3000 > /tmp/lua338.out 2> /tmp/lua338.err || rc=$?
kill $FPID 2>/dev/null || true
if [ "$rc" -ne 0 ] && grep -q "proto_version" /tmp/lua338.err /tmp/lua338.out; then
  echo "PASS: proto=2 rejected"
  exit 0
fi
echo "FAIL: rc=$rc"; cat /tmp/lua338.err /tmp/lua338.out; exit 1
