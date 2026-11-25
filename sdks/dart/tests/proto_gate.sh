#!/bin/sh
# #338 batch 2: a welcome advertising proto=2 must fail the connect with a
# proto_version error (missing proto = v1 tolerance).
# hello.version batch: the hello must carry "version" (fixture
# REQUIRE_VERSION) and a client_outdated error frame must surface the
# upgrade URL on stderr with a non-zero exit.
set -u
cd "$(dirname "$0")/.."
go build -o /tmp/wss338dart ./testtools/wssserver || { echo "FAIL: fixture build"; exit 1; }

ADDR=""
FPID=""
start_fixture() {  # $1 = ENV=VAL assignments; sets ADDR
  : > /tmp/wss338dart.addr  # truncate first: a stale non-empty file would defeat the -s wait
  env $1 /tmp/wss338dart -listen 127.0.0.1:0 -plain > /tmp/wss338dart.addr &
  FPID=$!
  i=0
  while [ ! -s /tmp/wss338dart.addr ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
  ADDR=$(head -1 /tmp/wss338dart.addr)
}
stop_fixture() { kill "$FPID" 2>/dev/null || true; wait "$FPID" 2>/dev/null || true; }
run_client() {    # $1 = timeout seconds; sets rc, captures /tmp/dart338.{out,err}
  rc=0
  ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 timeout "$1" dart run etunnel.dart 3000 > /tmp/dart338.out 2> /tmp/dart338.err || rc=$?
}
fail() { echo "FAIL: $1 (rc=$rc)"; cat /tmp/dart338.err /tmp/dart338.out /tmp/wss338dart.addr 2>/dev/null || true; stop_fixture; exit 1; }

echo "== fixture (ETUNNEL_FIXTURE_PROTO=2) =="
start_fixture "ETUNNEL_FIXTURE_PROTO=2"
run_client 20
stop_fixture
if [ "$rc" -ne 0 ] && grep -q "proto_version" /tmp/dart338.err /tmp/dart338.out; then
  echo "PASS: proto=2 rejected"
else
  fail "proto=2 not rejected"
fi

echo "== hello.version required (ETUNNEL_FIXTURE_REQUIRE_VERSION=1) =="
start_fixture "ETUNNEL_FIXTURE_REQUIRE_VERSION=1"
run_client 8
stop_fixture
if grep -q "HELLO_VERSION_MISSING" /tmp/wss338dart.addr; then
  fail "hello carried no version"
fi
echo "PASS: hello carried version"

echo "== client_outdated (ETUNNEL_FIXTURE_OUTDATED=1) =="
start_fixture "ETUNNEL_FIXTURE_OUTDATED=1"
run_client 20
stop_fixture
if [ "$rc" -ne 0 ] && grep -q "Upgrade: https://github.com/topcheer/etunnel" /tmp/dart338.err /tmp/dart338.out; then
  echo "PASS: client_outdated surfaced with the upgrade URL"
  exit 0
fi
fail "client_outdated not surfaced"
