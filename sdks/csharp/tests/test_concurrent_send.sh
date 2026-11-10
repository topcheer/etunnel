#!/bin/sh
# #54 regression ①: 24 concurrent tunnel requests racing Serve() plus pong
# replies used to hit ClientWebSocket's single-outstanding-send limit
# (InvalidOperationException / interleaved frames). The fixture enforces
# per-sid ordering (res before DATA/FIN) and the client must stay clean.
set -eu
cd "$(dirname "$0")/.."

echo "== build =="
dotnet build -v q 2>&1 | grep -E 'error|Build succeeded' | head -3

echo "== fixture: burst wss server + concurrent local app =="
go build -o /tmp/wssc-cs ./testtools/wssserver
python3 - <<'EOF' > /tmp/cs-local.log 2>&1 &
import socket, threading
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 39501)); s.listen(64)
print("listening", flush=True)
while True:
    c, _ = s.accept()
    def h(c=c):
        try:
            c.recv(65536)
            body = b"payload-" * 64  # 512B: forces several MAXP-sized chunks later
            c.sendall(b"HTTP/1.1 200 OK\r\ncontent-length: %d\r\n\r\n" % len(body) + body)
        except OSError: pass
        finally: c.close()
    threading.Thread(target=h, daemon=True).start()
EOF
LOCAL=$!
ADDR_FILE=$(mktemp)
( /tmp/wssc-cs -mode burst -plain >"$ADDR_FILE" 2>/dev/null ) &
FX=$!
trap 'kill $FX $LOCAL 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
ADDR=$(head -1 "$ADDR_FILE")

echo "== run =="
OUT=$(ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    ETUNNEL_LOCAL=39501 timeout 30 dotnet run --no-build 2>&1 || true)
echo "$OUT" | grep -E 'TUNNEL_URL|error|BURST' | sed 's/^/    /' || true

if echo "$OUT" | grep -qi 'InvalidOperationException'; then
    echo "FAIL: concurrent SendAsync raced"
    exit 1
fi
if ! grep -q 'BURST_OK' "$ADDR_FILE"; then
    echo "FAIL: fixture reports $(grep BURST_BAD "$ADDR_FILE" | head -1)"
    exit 1
fi
echo "PASS: concurrent_send"
