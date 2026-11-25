#!/bin/sh
# Hello / protocol gate for the C client, driven against the stub fixture
# (testtools/wssserver), which mirrors the real server's handshake answers:
#
#  1. welcome with proto=2  -> CLI exits non-zero with a proto_version error
#     (#338 batch 2; permanent failure, missing proto = v1 tolerance)
#  2. hello.version         -> the hello carries exactly ETUNNEL_VERSION
#     (stamped from VERSION by scripts/bump-release.py)
#  3. error client_outdated -> the server's message is printed verbatim on
#     stderr (it carries the upgrade URL) and the CLI exits non-zero
set -eu
cd "$(dirname "$0")/.."
echo "== build =="
make 2>&1 | tail -1
go build -o /tmp/wss338c ./testtools/wssserver
V=$(sed -n 's/^#define ETUNNEL_VERSION "\(.*\)"/\1/p' etunnel.c)
echo "client version: $V (VERSION file: $(cat ../../VERSION))"
[ "$V" = "$(cat ../../VERSION)" ] || { echo "FAIL: ETUNNEL_VERSION != VERSION"; exit 1; }
D=$(mktemp -d)
go build -o "$D/wss" ./testtools/wssserver

start_fixture() { # mode [VAR=VALUE...] ; sets ADDR / FPID
    mode=$1
    shift
    # Truncate in the parent: the background redirection races the wait loop
    # below and a stale address from the previous case would be read.
    : > "$D/addr"
    env "$@" "$D/wss" -mode "$mode" -listen 127.0.0.1:0 -plain >> "$D/addr" &
    FPID=$!
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do [ -s "$D/addr" ] && break; sleep 0.25; done
    [ -s "$D/addr" ] || { echo "FAIL: fixture did not start"; exit 1; }
    ADDR=$(head -1 "$D/addr")
}
stop_fixture() { kill "$FPID" 2>/dev/null || true; wait "$FPID" 2>/dev/null || true; }

echo "== 1) welcome with proto=2 -> proto_version, non-zero exit =="
start_fixture welcome ETUNNEL_FIXTURE_PROTO=2
rc=0
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 ETUNNEL_RECONNECT=0 timeout 10 ./etunnel 3000 >"$D/1.out" 2>"$D/1.err" || rc=$?
stop_fixture
if [ "$rc" -ne 0 ] && grep -q "proto_version" "$D/1.err"; then
    echo "PASS: proto=2 rejected: $(head -1 "$D/1.err")"
else
    echo "FAIL: rc=$rc stderr:"; cat "$D/1.err"; exit 1
fi

echo "== 2) hello.version == $V (fixture hellocheck) =="
start_fixture hellocheck ET_TEST_VERSION="$V"
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 ETUNNEL_RECONNECT=0 timeout 6 ./etunnel 3000 >"$D/2.out" 2>"$D/2.err" || true
stop_fixture
if grep -q "HELLO_VERSION_OK $V" "$D/addr" && grep -q '^TUNNEL_URL: http://fake.t.test' "$D/2.out"; then
    echo "PASS: $(grep HELLO_VERSION_OK "$D/addr"); client got its welcome"
else
    echo "FAIL: fixture said: $(grep HELLO "$D/addr" || echo nothing); client stderr:"; cat "$D/2.err"; exit 1
fi

echo "== 3) error client_outdated -> message verbatim, non-zero exit =="
MSG="etunnel client 0.0.1 is too old; this server runs 9.9.9 and requires SDK >= 1.0.0. Upgrade: https://github.com/topcheer/etunnel"
start_fixture outdated ET_TEST_OUTDATED_MSG="$MSG"
rc=0
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_LOCAL=3000 timeout 10 ./etunnel 3000 >"$D/3.out" 2>"$D/3.err" || rc=$?
stop_fixture
if [ "$rc" -ne 0 ] && grep -qF "$MSG" "$D/3.err" && ! grep -q '^TUNNEL_URL' "$D/3.out"; then
    echo "PASS: rc=$rc, stderr: $(grep -F "$MSG" "$D/3.err")"
else
    echo "FAIL: rc=$rc stderr:"; cat "$D/3.err"; echo "stdout:"; cat "$D/3.out"; exit 1
fi
