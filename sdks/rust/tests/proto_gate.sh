#!/bin/sh
# proto gate for the rust CLI (rust testtools/wssserver fixture):
#  1. the hello must carry "version" (fixture REQUIRE_VERSION)
#  2. a client_outdated error frame must surface the upgrade URL, non-zero exit
# welcome.proto=2 rejection is not asserted here (no gate in the rust SDK yet).
set -u
cd "$(dirname "$0")/.."
cargo build 2>&1 | tail -1
go build -o /tmp/wss338rust ./testtools/wssserver || { echo "FAIL: fixture build"; exit 1; }

ADDR=""
FPID=""
start_fixture() {  # $1 = ENV=VAL assignments; sets ADDR
  : > /tmp/wss338rust.addr  # truncate first: a stale non-empty file would defeat the -s wait
  env $1 /tmp/wss338rust -listen 127.0.0.1:0 -plain > /tmp/wss338rust.addr &
  FPID=$!
  i=0
  while [ ! -s /tmp/wss338rust.addr ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
  ADDR=$(head -1 /tmp/wss338rust.addr)
}
stop_fixture() { kill "$FPID" 2>/dev/null || true; wait "$FPID" 2>/dev/null || true; }
run_client() {    # $1 = timeout seconds; sets rc, captures /tmp/rust338.{out,err}
  rc=0
  ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 timeout "$1" ./target/debug/etunnel > /tmp/rust338.out 2> /tmp/rust338.err || rc=$?
}
fail() { echo "FAIL: $1 (rc=$rc)"; cat /tmp/rust338.err /tmp/rust338.out /tmp/wss338rust.addr 2>/dev/null || true; stop_fixture; exit 1; }

echo "== hello.version required (ETUNNEL_FIXTURE_REQUIRE_VERSION=1) =="
start_fixture "ETUNNEL_FIXTURE_REQUIRE_VERSION=1"
run_client 6
stop_fixture
if grep -q "HELLO_VERSION_MISSING" /tmp/wss338rust.addr; then
  fail "hello carried no version"
fi
echo "PASS: hello carried version"

echo "== client_outdated (ETUNNEL_FIXTURE_OUTDATED=1) =="
start_fixture "ETUNNEL_FIXTURE_OUTDATED=1"
run_client 20
stop_fixture
if [ "$rc" -ne 0 ] && grep -q "Upgrade: https://github.com/topcheer/etunnel" /tmp/rust338.err /tmp/rust338.out; then
  echo "PASS: client_outdated surfaced with the upgrade URL"
  exit 0
fi
fail "client_outdated not surfaced"
