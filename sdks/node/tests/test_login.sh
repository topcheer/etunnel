#!/usr/bin/env bash
# E2E for the CLI loopback login flow (sdks/node/etunnel.js).
# Fake server on loopback only (RFC 8252 loopback flow, plain HTTP by design).
set -u
cd "$(dirname "$0")/.."

PORT_FILE=$(mktemp)
node -e '
const http = require("http");
let redirect = "";
const srv = http.createServer((req, res) => {
  let body = "";
  req.on("data", c => body += c);
  req.on("end", () => {
    if (req.url === "/auth/cli/start") {
      const j = JSON.parse(body || "{}");
      if (!/^http:\/\/127\.0\.0\.1:\d+\/callback$/.test(j.redirect_uri || "")) {
        res.writeHead(400); res.end("bad redirect_uri"); return;
      }
      redirect = j.redirect_uri;
      res.writeHead(200, {"Content-Type": "application/json"});
      res.end(JSON.stringify({auth_url: "http://127.0.0.1:" + srv.address().port + "/auth/cli/grant?d=dev1", device_code: "dev1"}));
    } else if (req.url.startsWith("/auth/cli/grant")) {
      const cb = redirect + "?code=one-time-1";
      fetch(cb).then(r => console.error("fake-server: callback ->", r.status))
               .catch(e => console.error("fake-server: callback FAILED:", e.message));
      res.writeHead(200); res.end("approve page");
    } else if (req.url === "/auth/cli/exchange") {
      const j = JSON.parse(body || "{}");
      if (j.device_code !== "dev1" || j.code !== "one-time-1") {
        res.writeHead(400); res.end("bad exchange"); return;
      }
      res.writeHead(200, {"Content-Type": "application/json"});
      res.end(JSON.stringify({api_token: "tok-123", subdomains: ["my-sub"], plan: "pro", expires_at: 1730000000}));
      setTimeout(() => process.exit(0), 200);
    } else { res.writeHead(404); res.end(); }
  });
});
srv.listen(0, "127.0.0.1", () => {
  require("fs").writeFileSync(process.argv[1], String(srv.address().port));
});
' "$PORT_FILE" &
FAKE_PID=$!

for i in $(seq 1 50); do [ -s "$PORT_FILE" ] && break; sleep 0.1; done
FAKE_PORT=$(cat "$PORT_FILE" 2>/dev/null)
[ -n "$FAKE_PORT" ] || { echo "FAIL: fake server did not start"; kill $FAKE_PID 2>/dev/null; exit 1; }

RESULT=$(node -e "
const {login} = require('./etunnel.js');
login('http://127.0.0.1:$FAKE_PORT', {openBrowser: (u) => fetch(u), timeoutMs: 15000})
  .then(c => console.log('OK:' + c.api_token + ':' + (c.subdomains||[]).join(',') + ':' + c.plan))
  .catch(e => { console.error('ERR:' + e.message); process.exit(1); });
")
RC=$?
kill $FAKE_PID 2>/dev/null
rm -f "$PORT_FILE"

if [ $RC -ne 0 ]; then echo "FAIL: login errored"; exit 1; fi
case "$RESULT" in
  OK:tok-123:my-sub:pro) echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: unexpected result: $RESULT"; exit 1 ;;
esac
