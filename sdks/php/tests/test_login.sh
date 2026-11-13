#!/bin/sh
# E2E: CLI loopback login (sdks/php) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/php"

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }
export FX_ADDR=$ADDR
cat > /tmp/php_login_check.php <<'PHPEOF'
<?php
error_reporting(E_ALL & ~E_DEPRECATED);
require "etunnel.php";
$c = EtunnelLogin::login("http://" . getenv("FX_ADDR"),
  function ($u) { exec("curl -s -o /dev/null " . escapeshellarg($u) . " >/dev/null 2>&1 &"); return true; }, 30);
echo "CRED:" . $c["api_token"] . ":" . implode(",", $c["subdomains"]) . ":" . $c["plan"];
PHPEOF
RESULT=$(timeout 60 php /tmp/php_login_check.php 2>&1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
