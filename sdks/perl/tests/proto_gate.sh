#!/bin/sh
# proto gate for the perl CLI (same fixture as dart/lua):
#  1. the hello must carry "version" (fixture REQUIRE_VERSION)
#  2. a client_outdated error frame must surface the upgrade URL, non-zero exit
set -u
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
go build -o /tmp/wss338perl "$ROOT/dart/testtools/wssserver" || { echo "FAIL: fixture build"; exit 1; }

ADDR=""
FPID=""
start_fixture() {  # $1 = ENV=VAL assignments; sets ADDR
  : > /tmp/wss338perl.addr  # truncate first: a stale non-empty file would defeat the -s wait
  env $1 /tmp/wss338perl -listen 127.0.0.1:0 -plain > /tmp/wss338perl.addr &
  FPID=$!
  i=0
  while [ ! -s /tmp/wss338perl.addr ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
  ADDR=$(head -1 /tmp/wss338perl.addr)
}
stop_fixture() { kill "$FPID" 2>/dev/null || true; wait "$FPID" 2>/dev/null || true; }
run_client() {    # $1 = timeout seconds; sets rc, captures /tmp/perl338.{out,err}
  rc=0
  ETUNNEL_SERVER="ws://$ADDR/tunnel" timeout "$1" perl etunnel.pl 3000 > /tmp/perl338.out 2> /tmp/perl338.err || rc=$?
}
fail() { echo "FAIL: $1 (rc=$rc)"; cat /tmp/perl338.err /tmp/perl338.out /tmp/wss338perl.addr 2>/dev/null || true; stop_fixture; exit 1; }

# proto=2 rejection is NOT asserted here: perl/swift do not gate welcome.proto yet
# (#338 batch covered other SDKs) - tracked as a follow-up, out of this change's scope.

echo "== hello.version required (ETUNNEL_FIXTURE_REQUIRE_VERSION=1) =="
start_fixture "ETUNNEL_FIXTURE_REQUIRE_VERSION=1"
run_client 8
stop_fixture
if grep -q "HELLO_VERSION_MISSING" /tmp/wss338perl.addr; then
  fail "hello carried no version"
fi
echo "PASS: hello carried version"

echo "== client_outdated (ETUNNEL_FIXTURE_OUTDATED=1) =="
start_fixture "ETUNNEL_FIXTURE_OUTDATED=1"
run_client 10
stop_fixture
if [ "$rc" -ne 0 ] && grep -q "Upgrade: https://github.com/topcheer/etunnel" /tmp/perl338.err /tmp/perl338.out; then
  echo "PASS: client_outdated surfaced with the upgrade URL"
  exit 0
fi
fail "client_outdated not surfaced"
