#!/bin/sh
# #49 acceptance ①: a wss:// connection to a self-signed server MUST fail the
# handshake once certificate verification is on. Before the fix the same
# connection succeeded - that was the MITM exposure.
#
# Runs from sdks/cpp/. Requires: g++, libcurl, openssl, websocketpp, Go (for
# the fixture server); standalone asio 1.13 headers are fetched on first use
# (websocketpp 0.8.x does not build against newer asio releases).
set -eu
cd "$(dirname "$0")/.."

ASIO_DIR="${ASIO_DIR:-/tmp/et-cpp-tests/asio-asio-1-13-0/asio/include}"
if [ ! -f "$ASIO_DIR/asio.hpp" ]; then
    mkdir -p /tmp/et-cpp-tests
    curl -sSL --max-time 120 -o /tmp/et-cpp-tests/asio113.tar.gz \
        https://github.com/chriskohlhoff/asio/archive/refs/tags/asio-1-13-0.tar.gz
    tar xzf /tmp/et-cpp-tests/asio113.tar.gz -C /tmp/et-cpp-tests
fi

# asio 1.13 reads ASN1_STRING fields directly, which OpenSSL 4 made opaque;
# pin to an OpenSSL 3.x keg when available.
if [ -d /opt/homebrew/opt/openssl@3 ]; then
    OPENSSL_ROOT=/opt/homebrew/opt/openssl@3
else
    OPENSSL_ROOT="$(brew --prefix openssl 2>/dev/null || echo /usr)"
fi
WPP_INC="$(brew --prefix websocketpp 2>/dev/null || echo /usr/local)/include"

echo "== build =="
g++ -std=c++17 -DASIO_STANDALONE \
    -I"$WPP_INC" -I"$ASIO_DIR" -I"$OPENSSL_ROOT/include" \
    etunnel.cpp -o /tmp/etunnel-cpp-tlstest \
    -L"$OPENSSL_ROOT/lib" -lcurl -lssl -lcrypto -pthread
echo "build ok"

echo "== fixture: self-signed wss server =="
ADDR_FILE=$(mktemp)
( go run ./testtools/wssserver -mode welcome >"$ADDR_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s "$ADDR_FILE" ] && break
    sleep 1
done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture server did not start"; exit 1; }
ADDR=$(cat "$ADDR_FILE")
echo "fixture on $ADDR"

echo "== 1) default mode: self-signed MUST be rejected =="
OUT=$(ETUNNEL_SERVER="wss://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    timeout 15 /tmp/etunnel-cpp-tlstest 9 2>&1 || true)
echo "$OUT" | sed 's/^/    /'
if echo "$OUT" | grep -q "TUNNEL_URL"; then
    echo "FAIL: handshake succeeded against an untrusted certificate (MITM exposure)"
    exit 1
fi
echo "rejected as required"

echo "== 2) control: ETUNNEL_INSECURE_TLS=1 reaches the server (proves 1 was verification) =="
OUT=$(ETUNNEL_SERVER="wss://$ADDR/tunnel" ETUNNEL_RECONNECT=0 ETUNNEL_INSECURE_TLS=1 \
    timeout 5 /tmp/etunnel-cpp-tlstest 9 2>&1 || true)
if ! echo "$OUT" | grep -q "TUNNEL_URL"; then
    echo "FAIL: insecure escape hatch did not connect - rejection in 1 proved nothing"
    exit 1
fi
echo "$OUT" | grep "TUNNEL_URL\|WARNING" | sed 's/^/    /'
echo "control connected"
echo "PASS: tls_reject"
