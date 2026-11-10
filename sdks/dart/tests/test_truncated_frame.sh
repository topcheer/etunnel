#!/bin/sh
# #53 Dart regression: a truncated binary frame (declared length > payload)
# used to throw an unhandled RangeError and kill the isolate. The trunc
# fixture sends exactly that; the client must clamp, answer the request and
# survive to a clean one-shot exit on SIGTERM-style close.
set -eu
cd "$(dirname "$0")/.."

dart analyze etunnel.dart 2>&1 | tail -1

# fixture shared with the rust tests (#53 batch)
dart compile exe etunnel.dart -o /tmp/et-dart-exe 2>&1 | tail -1
(cd ../rust/testtools/wssserver && go build -o /tmp/wssc-dart .) || exit 1
python3 - <<'EOF' > /tmp/dt-local.log 2>&1 &
import socket, threading
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39801)); s.listen(8)
while True:
    c, _ = s.accept()
    def h(c=c):
        try:
            c.recv(65536)
            b = b"trunc-ok"
            c.sendall(b"HTTP/1.1 200 OK\r\ncontent-length: %d\r\n\r\n" % len(b) + b)
        except OSError: pass
        finally: c.close()
    threading.Thread(target=h, daemon=True).start()
EOF
LOCAL=$!
ADDR_FILE=$(mktemp)
( /tmp/wssc-dart -mode trunc -plain >"$ADDR_FILE" 2>/dev/null ) &
FX=$!
trap 'kill $FX $LOCAL 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
ADDR=$(head -1 "$ADDR_FILE")

OUT=$(ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    ETUNNEL_LOCAL=39801 timeout 25 /tmp/et-dart-exe 2>&1 || true)
echo "$OUT" | grep -E 'TUNNEL_URL|RangeError|Unhandled' | head -3 | sed 's/^/    /'
if echo "$OUT" | grep -q 'RangeError'; then
    echo "FAIL: truncated frame raised RangeError"
    exit 1
fi
if ! grep -q 'TRUNC_OK' "$ADDR_FILE"; then
    echo "FAIL: client did not survive to complete the response ($(cat "$ADDR_FILE" | tail -1))"
    exit 1
fi
echo "PASS: dart_truncated_frame"
