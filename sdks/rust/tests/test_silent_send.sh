#!/bin/sh
# #53 Rust regression: the reader held the socket lock across a blocking
# read(), so during server silence every outbound send (serve res, pong)
# waited for the next inbound frame - possibly forever. The sink fixture
# sends req+FIN and then NOTHING: the client's res must still arrive.
set -eu
cd "$(dirname "$0")/.."

echo "== build =="
cargo build -q 2>&1 | grep -E '^error' | head -3 || true

echo "== fixture: silent sink + local app =="
go build -o /tmp/wssc-rs ./testtools/wssserver
python3 - <<'EOF' > /tmp/rs-local.log 2>&1 &
import socket, threading
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39701)); s.listen(8)
while True:
    c, _ = s.accept()
    def h(c=c):
        try:
            c.recv(65536)
            b = b"silence-test-body"
            c.sendall(b"HTTP/1.1 200 OK\r\ncontent-length: %d\r\n\r\n" % len(b) + b)
        except OSError: pass
        finally: c.close()
    threading.Thread(target=h, daemon=True).start()
EOF
LOCAL=$!
ADDR_FILE=$(mktemp)
( /tmp/wssc-rs -mode sink -plain >"$ADDR_FILE" 2>/dev/null ) &
FX=$!
trap 'kill $FX $LOCAL 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
ADDR=$(head -1 "$ADDR_FILE")

echo "== run (server silent after req+FIN; res must arrive within seconds) =="
OUT=$(ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    ETUNNEL_LOCAL=39701 timeout 20 ./target/debug/etunnel 2>&1 || true)
echo "$OUT" | grep -E 'TUNNEL_URL' | sed 's/^/    /'
if ! grep -q 'RES_RECEIVED' "$ADDR_FILE"; then
    echo "FAIL: res never arrived while the server was silent (send blocked by reader lock)"
    exit 1
fi
echo "sink: $(grep RES_RECEIVED "$ADDR_FILE")"
echo "PASS: rust_silent_send"
