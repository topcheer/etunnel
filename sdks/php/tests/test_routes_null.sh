#!/bin/sh
# #430 (part 2) regression: Etunnel::connect(server, null, routes: [...]) - the
# README "Multi-target routing" form - used to fatal on the FIRST request:
#   Etunnel::serve(): Argument #5 ($local) must be of type string, null given
# Runs tests/routes_null_embed.php (local = null; "/api" -> 127.0.0.1:<p> with
# strip, "/" -> the harness's local service on 3000) against the local harness
# and checks both routes end to end, then that the process is still alive.
#
# Run (harness up, see /tmp/et-harness.sh up):  sh sdks/php/tests/test_routes_null.sh
# ET_PHP_SDK_DIR=<dir containing etunnel.php> tests another copy (fail-before
# against the published vendor tree). Exit 77 = harness not reachable.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
SDK=${ET_PHP_SDK_DIR:-"$HERE/.."}
API_PORT=${API_PORT:-39701}

if [ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' -H 'Host: localhost' http://127.0.0.1:8080/)" != 200 ]; then
    echo "SKIP: harness not reachable (/tmp/et-harness.sh up)"; exit 77
fi
if ! curl -s -m 3 http://127.0.0.1:3000/ | grep -q ET-LOCAL-OK; then
    echo "SKIP: harness local service on :3000 not reachable"; exit 77
fi

# second backend so the two routes are distinguishable: "ET-3001-OK path=<path>"
PY_SRC=$(mktemp)
cat > "$PY_SRC" <<'EOF'
import http.server, socketserver, sys
port = int(sys.argv[1])
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        b = f"ET-3001-OK path={self.path}\n".encode()
        self.send_response(200); self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", port), H) as s:
    s.serve_forever()
EOF
python3 "$PY_SRC" "$API_PORT" 2>/dev/null &
PY=$!
PHP=""
OUT=$(mktemp)
cleanup() { kill $PY $PHP 2>/dev/null || true; wait $PY $PHP 2>/dev/null || true; rm -f "$OUT" "$PY_SRC"; }
trap cleanup EXIT
sleep 0.5

ETUNNEL_SERVER=ws://localhost:8080/tunnel ET_PHP_SDK_DIR="$SDK" php -d error_reporting=E_ALL "$HERE/routes_null_embed.php" "$API_PORT" \
    > "$OUT" 2>&1 &
PHP=$!
for _ in $(seq 1 60); do grep -q TUNNEL_URL "$OUT" 2>/dev/null && break; kill -0 $PHP 2>/dev/null || break; sleep 0.25; done
URL=$(grep -o 'http://[a-z0-9-]*\.localhost' "$OUT" | head -1)
if [ -z "$URL" ]; then echo "FAIL: no TUNNEL_URL; output:"; cat "$OUT"; exit 1; fi
HOST=${URL#http://}
echo "tunnel: $URL"

probe() { curl -s -m 10 -H "Host: $HOST" "http://127.0.0.1:8080$1"; }

R1=$(probe "/api/echo?x=1"); echo "GET /api/echo?x=1 -> $R1"
echo "$R1" | grep -q "^ET-3001-OK path=/echo?x=1$" || { echo "FAIL: /api not routed to the api backend with strip (query carried)"; cat "$OUT"; exit 1; }
kill -0 $PHP 2>/dev/null || { echo "FAIL: process died on the first request"; cat "$OUT"; exit 1; }

R2=$(probe "/other"); echo "GET /other -> $R2"
echo "$R2" | grep -q "^ET-LOCAL-OK path=/other$" || { echo "FAIL: '/' fallback not routed to the local service"; cat "$OUT"; exit 1; }
kill -0 $PHP 2>/dev/null || { echo "FAIL: process died on the second request"; cat "$OUT"; exit 1; }
if grep -q "Fatal error\|TypeError" "$OUT"; then echo "FAIL: fatal in output:"; grep "Fatal\|TypeError" "$OUT"; exit 1; fi
echo "PASS: local=null + routes serves both routes with no TypeError (#430)"
