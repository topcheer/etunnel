#!/bin/sh
# #48 regression ①: a detached serve thread holding the raw wsi wrote freed
# memory once the connection dropped mid-curl (up to 120s window). Now serve
# threads only enqueue; the service loop drains. Reconnect stays ON so the
# process outlives the dead connection and the stale write would be caught.
set -eu
cd "$(dirname "$0")/.."

echo "== build (ASan) =="
PKGS="libwebsockets libcurl json-c openssl"
CFLAGS="$(pkg-config --cflags $PKGS 2>/dev/null)"
LIBS="$(pkg-config --libs $PKGS 2>/dev/null || echo -lwebsockets -lcurl -ljson-c)"
cc -std=c11 -Wall -Wextra -fsanitize=address $CFLAGS etunnel.c -o /tmp/etunnel-c-asan $LIBS -pthread
echo "build ok"

echo "== fixture: slow local target (6s) =="
python3 - <<'EOF' &
import socket, threading, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39402)); s.listen(4)
print("39402", flush=True)
while True:
    c, _ = s.accept()
    def h(c=c):
        try:
            c.recv(65536); time.sleep(6)
            c.sendall(b"HTTP/1.1 200 OK\r\ncontent-length: 2\r\n\r\nok")
        except OSError:
            pass
        finally:
            c.close()
    threading.Thread(target=h, daemon=True).start()
EOF
LOCAL=$!
trap 'kill $LOCAL 2>/dev/null || true' EXIT
sleep 1

echo "== fixture: drama wss server (welcome, req+FIN, drop @300ms; loops per reconnect) =="
ADDR_FILE=$(mktemp)
( go run ./testtools/wssserver -mode drama -plain >"$ADDR_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$ADDR_FILE" ] && break; sleep 1; done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture did not start"; exit 1; }
ADDR=$(cat "$ADDR_FILE")

echo "== run 20s (multiple connect/drop cycles; stale writes would land on freed wsi) =="
OUT=$(ASAN_OPTIONS=detect_leaks=0 ETUNNEL_SERVER="ws://$ADDR/tunnel" \
    ETUNNEL_LOCAL=39402 timeout 20 /tmp/etunnel-c-asan 2>&1 || true)
CYCLES=$(echo "$OUT" | grep -c "TUNNEL_URL" || true)
echo "connect cycles: $CYCLES"
echo "$OUT" | grep -E 'Sanitizer|slot collision' | head -3 | sed 's/^/    /' || true

if echo "$OUT" | grep -q "AddressSanitizer"; then
    echo "FAIL: use-after-free reported"
    exit 1
fi
[ "$CYCLES" -ge 5 ] || { echo "FAIL: expected several reconnect cycles, saw $CYCLES"; exit 1; }
echo "PASS: uaf_disconnect_asan"
