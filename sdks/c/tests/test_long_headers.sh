#!/bin/sh
# #48 regression ②: a local app answering with >64KB of response headers used
# to be memcpy'd into a 64KB stack buffer in send_ws - a stack smash. Now all
# outbound messages are heap-allocated and queued. Build with ASan and assert
# the client survives and the server-side sink receives the full res.
set -eu
cd "$(dirname "$0")/.."

echo "== build (ASan) =="
PKGS="libwebsockets libcurl json-c openssl"
CFLAGS="$(pkg-config --cflags $PKGS 2>/dev/null)"
LIBS="$(pkg-config --libs $PKGS 2>/dev/null || echo -lwebsockets -lcurl -ljson-c)"
cc -std=c11 -Wall -Wextra -fsanitize=address $CFLAGS etunnel.c -o /tmp/etunnel-c-asan $LIBS -pthread
echo "build ok"

echo "== fixture: local app with 70KB of response headers =="
python3 - <<'EOF' &
import socket, threading
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39401)); s.listen(4)
print("39401", flush=True)
while True:
    c, _ = s.accept()
    def h(c=c):
        try:
            c.recv(65536)
            hdrs = "".join("X-Pad-%04d: %s\r\n" % (i, "v" * 60) for i in range(1000))
            body = b"ok"
            c.sendall(("HTTP/1.1 200 OK\r\n" + hdrs + "content-length: 2\r\n\r\n").encode() + body)
        except OSError:
            pass
        finally:
            c.close()
    threading.Thread(target=h, daemon=True).start()
EOF
LOCAL=$!
trap 'kill $LOCAL 2>/dev/null || true' EXIT
sleep 1

echo "== fixture: sink wss server =="
ADDR_FILE=$(mktemp)
( go run ./testtools/wssserver -mode sink -plain >"$ADDR_FILE" 2>/dev/null ) &
FIXTURE=$!
trap 'kill $FIXTURE 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$ADDR_FILE" ] && break; sleep 1; done
[ -s "$ADDR_FILE" ] || { echo "FAIL: fixture did not start"; exit 1; }
ADDR=$(cat "$ADDR_FILE")

echo "== run =="
OUT=$(ASAN_OPTIONS=detect_leaks=0 ETUNNEL_SERVER="ws://$ADDR/tunnel" \
    ETUNNEL_RECONNECT=0 ETUNNEL_LOCAL=39401 timeout 30 /tmp/etunnel-c-asan 2>&1 || true)
echo "$OUT" | grep -E 'TUNNEL_URL|Sanitizer' | sed 's/^/    /' || true

if echo "$OUT" | grep -q "AddressSanitizer"; then
    echo "FAIL: sanitizer report (stack smash or worse)"
    exit 1
fi
if ! grep -q 'RES_RECEIVED' "$ADDR_FILE"; then
    echo "FAIL: sink never got the response"
    exit 1
fi
echo "sink: $(grep RES_RECEIVED "$ADDR_FILE")"
echo "PASS: long_headers_no_stack_smash"
