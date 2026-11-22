#!/bin/sh
# #338 batch 2: a welcome advertising proto=2 must fail the connect with a
# proto_version error (missing proto = v1 tolerance).
set -eu
cd "$(dirname "$0")/.."
echo "== fixture (ETUNNEL_FIXTURE_PROTO=2) =="
go build -o /tmp/wss338cs ./testtools/wssserver
ETUNNEL_FIXTURE_PROTO=2 /tmp/wss338cs -listen 127.0.0.1:0 -plain > /tmp/wss338cs.addr &
FPID=$!
sleep 0.5
ADDR=$(head -1 /tmp/wss338cs.addr)
echo "== client =="
ETUNNEL_SERVER="ws://$ADDR/tunnel" timeout 60 dotnet run --project tests/proto_gate 2>&1 | tee /tmp/cs338.log | tail -1
grep -q "PASS: proto=2 rejected" /tmp/cs338.log || { kill $FPID 2>/dev/null || true; exit 1; }
kill $FPID 2>/dev/null || true
