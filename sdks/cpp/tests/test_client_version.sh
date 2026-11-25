#!/bin/sh
# Hello / protocol gate for the C++ client against the stub fixture
# (testtools/wssserver, TLS with a throwaway certificate the client trusts
# through ETUNNEL_CA_FILE - this SDK is wss-only):
#
#  1. hello.version         -> the hello carries exactly ETUNNEL_VERSION
#     (stamped from VERSION by scripts/bump-release.py)
#  2. error client_outdated -> the server's message is printed verbatim on
#     stderr (it carries the upgrade URL) and the CLI exits non-zero
set -eu
cd "$(dirname "$0")/.."

WPP_INC="$(brew --prefix websocketpp 2>/dev/null || echo /usr/local)/include"
ASIO_DIR="${ASIO_DIR:-/tmp/et-cpp-tests/asio-asio-1-13-0/asio/include}"
if [ ! -f "$ASIO_DIR/asio.hpp" ]; then
    mkdir -p /tmp/et-cpp-tests
    curl -sSL --max-time 120 -o /tmp/et-cpp-tests/asio113.tar.gz \
        https://github.com/chriskohlhoff/asio/archive/refs/tags/asio-1-13-0.tar.gz
    tar xzf /tmp/et-cpp-tests/asio113.tar.gz -C /tmp/et-cpp-tests
fi
if [ -d /opt/homebrew/opt/openssl@3 ]; then
    OPENSSL_ROOT=/opt/homebrew/opt/openssl@3
else
    OPENSSL_ROOT="$(brew --prefix openssl 2>/dev/null || echo /usr)"
fi

echo "== build =="
g++ -std=c++17 -DASIO_STANDALONE -I"$WPP_INC" -I"$ASIO_DIR" -I"$OPENSSL_ROOT/include" \
    etunnel.cpp -o /tmp/etunnel-cpp-ver \
    -L"$OPENSSL_ROOT/lib" -lcurl -lssl -lcrypto -pthread
go build -o /tmp/wss-cpp-ver ./testtools/wssserver
V=$(sed -n 's/^#define ETUNNEL_VERSION "\(.*\)"/\1/p' etunnel.cpp)
echo "client version: $V (VERSION file: $(cat ../../VERSION))"
[ "$V" = "$(cat ../../VERSION)" ] || { echo "FAIL: ETUNNEL_VERSION != VERSION"; exit 1; }

echo "== throwaway certificate for localhost =="
D=$(mktemp -d)
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$D/key.pem" -out "$D/cert.pem" \
    -days 1 -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1

start_fixture() { # mode [VAR=VALUE...] ; sets ADDR / FPID
    mode=$1
    shift
    : > "$D/addr"
    env "$@" /tmp/wss-cpp-ver -mode "$mode" -listen 127.0.0.1:0 -cert "$D/cert.pem" -key "$D/key.pem" >> "$D/addr" &
    FPID=$!
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do [ -s "$D/addr" ] && break; sleep 0.25; done
    [ -s "$D/addr" ] || { echo "FAIL: fixture did not start"; exit 1; }
    ADDR=$(head -1 "$D/addr")
}
stop_fixture() { kill "$FPID" 2>/dev/null || true; wait "$FPID" 2>/dev/null || true; }

echo "== 1) hello.version == $V (fixture hellocheck) =="
start_fixture hellocheck ET_TEST_VERSION="$V"
ETUNNEL_SERVER="wss://$ADDR/tunnel" ETUNNEL_LOCAL=3000 ETUNNEL_RECONNECT=0 \
    ETUNNEL_CA_FILE="$D/cert.pem" timeout 6 /tmp/etunnel-cpp-ver 3000 >"$D/1.out" 2>"$D/1.err" || true
stop_fixture
if grep -q "HELLO_VERSION_OK $V" "$D/addr" && grep -q '^TUNNEL_URL: http://fake.t.test' "$D/1.out"; then
    echo "PASS: $(grep HELLO_VERSION_OK "$D/addr"); client got its welcome"
else
    echo "FAIL: fixture said: $(grep HELLO "$D/addr" || echo nothing); client stderr:"; cat "$D/1.err"; exit 1
fi

echo "== 2) error client_outdated -> message verbatim, non-zero exit =="
MSG="etunnel client 0.0.1 is too old; this server runs 9.9.9 and requires SDK >= 1.0.0. Upgrade: https://github.com/topcheer/etunnel"
start_fixture outdated ET_TEST_OUTDATED_MSG="$MSG"
rc=0
ETUNNEL_SERVER="wss://$ADDR/tunnel" ETUNNEL_LOCAL=3000 \
    ETUNNEL_CA_FILE="$D/cert.pem" timeout 10 /tmp/etunnel-cpp-ver 3000 >"$D/2.out" 2>"$D/2.err" || rc=$?
stop_fixture
if [ "$rc" -ne 0 ] && LC_ALL=C grep -a -qF "$MSG" "$D/2.err" && ! grep -a -q '^TUNNEL_URL' "$D/2.out"; then
    echo "PASS: rc=$rc, stderr: $(LC_ALL=C grep -a -F "$MSG" "$D/2.err")"
else
    echo "FAIL: rc=$rc stderr:"; cat "$D/2.err"; echo "stdout:"; cat "$D/2.out"; exit 1
fi
