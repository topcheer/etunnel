#!/usr/bin/env bash
# E2E for the CLI loopback login flow (sdks/python/etunnel.py).
# Fake server on loopback only (RFC 8252 loopback flow, plain HTTP by design).
set -u
cd "$(dirname "$0")/.."

PORT_FILE=$(mktemp)
python3 - "$PORT_FILE" <<'PY' &
import json, re, subprocess, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
redirect = {"uri": ""}

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n) or b"{}")
        if self.path == "/auth/cli/start":
            if not re.match(r"^http://127\.0\.0\.1:\d+/callback$", body.get("redirect_uri", "")):
                return self._json({"error": "bad redirect_uri"}, 400)
            redirect["uri"] = body["redirect_uri"]
            self._json({"auth_url": "http://127.0.0.1:%d/auth/cli/grant?d=dev1" % self.server.server_address[1],
                        "device_code": "dev1"})
        elif self.path == "/auth/cli/exchange":
            if body.get("device_code") != "dev1" or body.get("code") != "one-time-1":
                return self._json({"error": "bad exchange"}, 400)
            self._json({"api_token": "tok-123", "subdomains": ["my-sub"], "plan": "pro", "expires_at": 1730000000})
            threading.Timer(0.3, lambda: os._exit(0)).start()
        else:
            self.send_response(404); self.end_headers()
    def do_GET(self):
        if self.path.startswith("/auth/cli/grant"):
            subprocess.Popen(["curl", "-s", "-o", "/dev/null", redirect["uri"] + "?code=one-time-1"])
            self.send_response(200); self.end_headers(); self.wfile.write(b"approve")
        else:
            self.send_response(404); self.end_headers()

import os, threading
srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY
FAKE_PID=$!

for i in $(seq 1 50); do [ -s "$PORT_FILE" ] && break; sleep 0.1; done
FAKE_PORT=$(cat "$PORT_FILE" 2>/dev/null)
[ -n "$FAKE_PORT" ] || { echo "FAIL: fake server did not start"; kill $FAKE_PID 2>/dev/null; exit 1; }

RESULT=$(python3 -c "
import etunnel
try:
    c = etunnel.login('http://127.0.0.1:$FAKE_PORT', {'open_browser': (lambda u: __import__('urllib.request', fromlist=['urlopen']).urlopen(u)), 'timeout': 15})
    print('OK:%s:%s:%s' % (c['api_token'], ','.join(c['subdomains']), c['plan']))
except Exception as e:
    print('ERR:%s' % e); raise SystemExit(1)")
RC=$?
kill $FAKE_PID 2>/dev/null
rm -f "$PORT_FILE"

if [ $RC -ne 0 ]; then echo "FAIL: login errored: $RESULT"; exit 1; fi
case "$RESULT" in
  OK:tok-123:my-sub:pro) echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: unexpected result: $RESULT"; exit 1 ;;
esac
