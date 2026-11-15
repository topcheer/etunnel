#!/bin/sh
# E2E: CLI loopback login (sdks/swift, macOS) via `swift test` against the
# shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/swift"
(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

if FX_ADDR=$ADDR timeout 600 swift test --filter LoginE2E 2>&1 | tail -4 | grep -qE 'passed|Executed 1 test.*0 failures'; then
  echo "PASS: loopback login start->grant->exchange"
else
  echo "FAIL: swift test LoginE2E"
  FX_ADDR=$ADDR timeout 600 swift test --filter LoginE2E 2>&1 | tail -8
  exit 1
fi
