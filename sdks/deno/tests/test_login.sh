#!/bin/sh
# E2E: CLI loopback login (sdks/deno) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/deno"
(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }
export FX_ADDR=$ADDR
RESULT=$(timeout 60 deno run --allow-net --allow-read --allow-env --allow-run tests/login_check.ts 2>&1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  *'CRED:tok-123:my-sub:pro'*) echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
