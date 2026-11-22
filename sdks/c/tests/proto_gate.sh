#!/bin/sh
# #338 batch 2: a welcome advertising proto=2 must terminate the CLI with a
# proto_version error (permanent failure; missing proto = v1 tolerance).
set -eu
cd "$(dirname "$0")/.."
echo "== build =="
make 2>&1 | tail -1
echo "== fixture (ETUNNEL_FIXTURE_PROTO=2) =="
go build -o /tmp/wss338c ./testtools/wssserver
ETUNNEL_FIXTURE_PROTO=2 /tmp/wss338c -listen 127.0.0.1:0 -plain > /tmp/wss338c.addr &
FPID=$!
sleep 0.5
ADDR=$(head -1 /tmp/wss338c.addr)
echo "== client =="
rc=0
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 timeout 10 ./etunnel 3000 > /tmp/c338.out 2> /tmp/c338.err || rc=$?
kill $FPID 2>/dev/null || true
if [ "$rc" -ne 0 ] && grep -q "proto_version" /tmp/c338.err; then
  echo "PASS: proto=2 rejected: $(head -1 /tmp/c338.err)"
  exit 0
fi
echo "FAIL: rc=$rc stderr:"; cat /tmp/c338.err; exit 1
