#!/bin/sh
# #49 acceptance ②: the serve thread outlives the websocket connection by up
# to 120s (curl). This drives exactly that race under AddressSanitizer:
# connect, receive a request whose local target sleeps, then kill the
# connection while curl is still waiting. The client must exit cleanly - no
# use-after-free, no sanitizer report.
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

echo "== build (ASan + UBSan) =="
g++ -std=c++17 -DASIO_STANDALONE -fsanitize=address,undefined \
    -I"$WPP_INC" -I"$ASIO_DIR" -I"$OPENSSL_ROOT/include" \
    etunnel.cpp -o /tmp/etunnel-cpp-asan \
    -L"$OPENSSL_ROOT/lib" -lcurl -lssl -lcrypto -pthread
echo "build ok"

echo "== fixture: slow local target (accepts, sleeps 6s, answers) =="
python3 - <<'EOF' &
import socket, time, sys
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39391)); s.listen(4)
sys.stdout.write("39391\n"); sys.stdout.flush()
while True:
    c, _ = s.accept()
    def handle(c=c):
        try:
            c.recv(65536); time.sleep(6)
            c.sendall(b"HTTP/1.1 200 OK\r\ncontent-length: 2\r\n\r\nok")
        except OSError:
            pass
        finally:
            c.close()
    import threading; threading.Thread(target=handle, daemon=True).start()
EOF
SLOW=$!
trap 'kill $SLOW 2>/dev/null || true' EXIT
sleep 1

echo "== fixture: drama wss server (welcome, req+FIN, drop after 300ms) =="
ADDR_FILE=$(mktemp)
( go run ./testtools/wssserver -mode drama >"$ADDR_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s "$ADDR_FILE" ] && break
    sleep 1
done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture server did not start"; exit 1; }
ADDR=$(cat "$ADDR_FILE")

echo "== run: connect via insecure TLS, tunnel req to the slow target, connection dies mid-curl =="
OUT=$(ASAN_OPTIONS=detect_leaks=0 \
    ETUNNEL_SERVER="wss://$ADDR/tunnel" ETUNNEL_RECONNECT=0 ETUNNEL_INSECURE_TLS=1 \
    ETUNNEL_LOCAL=39391 timeout 40 /tmp/etunnel-cpp-asan 2>&1 || true)
echo "$OUT" | sed 's/^/    /'

if echo "$OUT" | grep -q "AddressSanitizer\|runtime error\|SUMMARY: .*Sanitizer"; then
    echo "FAIL: sanitizer reported an error (use-after-free / UB)"
    exit 1
fi
echo "PASS: uaf_disconnect_asan"
