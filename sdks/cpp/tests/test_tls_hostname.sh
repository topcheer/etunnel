#!/bin/sh
# #49/#65: verify_peer proves the chain, not the identity. A MITM holding a
# CA-signed certificate for ANY domain must NOT pass when we dialed 127.0.0.1.
#
#  1. wrong-SAN  (SAN=evil.example, signed by a CA the client trusts):
#     handshake must FAIL (hostname mismatch despite a valid chain)
#  2. right-SAN  (SAN=127.0.0.1, same CA): handshake must SUCCEED
#     (control: the chain machinery itself works)
#  3. falsified build (set1_host removed, everything else identical):
#     wrong-SAN must SUCCEED - proving the test bites exactly the hostname gap
set -eu
cd "$(dirname "$0")/.."

WPP_INC="$(brew --prefix websocketpp 2>/dev/null || echo /usr/local)/include"
ASIO_DIR="${ASIO_DIR:-/tmp/et-cpp-tests/asio-asio-1-13-0/asio/include}"
if [ -d /opt/homebrew/opt/openssl@3 ]; then
    OPENSSL_ROOT=/opt/homebrew/opt/openssl@3
else
    OPENSSL_ROOT="$(brew --prefix openssl 2>/dev/null || echo /usr)"
fi

echo "== build (host-pinned) =="
g++ -std=c++17 -DASIO_STANDALONE -I"$WPP_INC" -I"$ASIO_DIR" -I"$OPENSSL_ROOT/include" \
    etunnel.cpp -o /tmp/etunnel-cpp-hn \
    -L"$OPENSSL_ROOT/lib" -lcurl -lssl -lcrypto -pthread

echo "== falsified build (hostname pin compiled out) =="
cp etunnel.cpp /tmp/etunnel-falsy.cpp
perl -0pi -e 's/X509_VERIFY_PARAM_set1_host\(param, tls_host\.c_str\(\), tls_host\.size\(\)\)/1 \/* FALSIFY *\//' /tmp/etunnel-falsy.cpp
perl -0pi -e 's/X509_VERIFY_PARAM_set1_ip_asc\(param, tls_host\.c_str\(\)\)/1 \/* FALSIFY *\//' /tmp/etunnel-falsy.cpp
grep -c FALSIFY /tmp/etunnel-falsy.cpp
g++ -std=c++17 -DASIO_STANDALONE -I"$WPP_INC" -I"$ASIO_DIR" -I"$OPENSSL_ROOT/include" \
    /tmp/etunnel-falsy.cpp -o /tmp/etunnel-cpp-falsy \
    -L"$OPENSSL_ROOT/lib" -lcurl -lssl -lcrypto -pthread

echo "== build fixture =="
go build -o /tmp/wsspp-cpp ./testtools/wssserver

echo "== test CA + two leaves =="
DIR=$(mktemp -d)
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$DIR/ca.key" -out "$DIR/ca.pem" \
    -days 1 -subj "/CN=etunnel-test-ca" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -keyout "$DIR/wrong.key" -out "$DIR/wrong.csr" \
    -subj "/CN=evil.example" >/dev/null 2>&1
printf "subjectAltName=DNS:evil.example\n" > "$DIR/wrong.ext"
openssl x509 -req -in "$DIR/wrong.csr" -CA "$DIR/ca.pem" -CAkey "$DIR/ca.key" -CAcreateserial \
    -out "$DIR/wrong.pem" -days 1 -extfile "$DIR/wrong.ext" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -keyout "$DIR/right.key" -out "$DIR/right.csr" \
    -subj "/CN=localhost" >/dev/null 2>&1
printf "subjectAltName=IP:127.0.0.1,DNS:localhost\n" > "$DIR/right.ext"
openssl x509 -req -in "$DIR/right.csr" -CA "$DIR/ca.pem" -CAkey "$DIR/ca.key" -CAcreateserial \
    -out "$DIR/right.pem" -days 1 -extfile "$DIR/right.ext" >/dev/null 2>&1
ls "$DIR"/*.pem | wc -l

run_client() { # binary, port
    ETUNNEL_SERVER="wss://127.0.0.1:$2/tunnel" ETUNNEL_RECONNECT=0 \
        ETUNNEL_CA_FILE="$DIR/ca.pem" timeout 8 "$1" 9 2>&1 || true
}

echo "== 1) wrong-SAN @ host-pinned build: must REJECT =="
ADDR_FILE=$(mktemp)
/tmp/wsspp-cpp -mode welcome -cert "$DIR/wrong.pem" -key "$DIR/wrong.key" >"$ADDR_FILE" 2>/dev/null &
FX=$!
trap 'kill $FX 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
PORT=$(head -1 "$ADDR_FILE" | sed 's/.*://')
OUT=$(run_client /tmp/etunnel-cpp-hn "$PORT")
echo "$OUT" | grep -m1 -E 'TLS handshake failed|error' | sed 's/^/    /' || true
if echo "$OUT" | grep -q TUNNEL_URL; then echo "FAIL: wrong-SAN certificate accepted"; exit 1; fi
echo "rejected as required"
kill $FX 2>/dev/null

echo "== 2) right-SAN control: must CONNECT =="
ADDR_FILE=$(mktemp)
/tmp/wsspp-cpp -mode welcome -cert "$DIR/right.pem" -key "$DIR/right.key" >"$ADDR_FILE" 2>/dev/null &
FX=$!
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
PORT=$(head -1 "$ADDR_FILE" | sed 's/.*://')
OUT=$(run_client /tmp/etunnel-cpp-hn "$PORT")
echo "$OUT" | grep -m1 TUNNEL_URL | sed 's/^/    /' || true
if ! echo "$OUT" | grep -q TUNNEL_URL; then echo "FAIL: right-SAN certificate rejected (control broken)"; exit 1; fi
echo "control connected"
kill $FX 2>/dev/null

echo "== 3) falsified (no hostname pin): wrong-SAN must CONNECT =="
ADDR_FILE=$(mktemp)
/tmp/wsspp-cpp -mode welcome -cert "$DIR/wrong.pem" -key "$DIR/wrong.key" >"$ADDR_FILE" 2>/dev/null &
FX=$!
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
PORT=$(head -1 "$ADDR_FILE" | sed 's/.*://')
OUT=$(run_client /tmp/etunnel-cpp-falsy "$PORT")
echo "$OUT" | grep -m1 TUNNEL_URL | sed 's/^/    /' || true
if ! echo "$OUT" | grep -q TUNNEL_URL; then echo "FAIL: falsified build also rejected - test proves nothing"; exit 1; fi
echo "falsification holds: hostname pin was the only difference"
kill $FX 2>/dev/null

echo "PASS: tls_hostname"
