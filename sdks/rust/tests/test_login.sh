#!/bin/sh
# E2E: CLI loopback login (sdks/rust) via `cargo test` against the shared
# Go loginserver fixture. The test lives in src/main.rs (#[cfg(test)] mod
# login_e2e) — bin-crate root functions are only reachable from there.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/rust"

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

if FX_ADDR=$ADDR timeout 300 cargo test loopback_login 2>&1 | tail -3 | grep -q 'test result: ok'; then
  echo "PASS: loopback login start->grant->exchange"
else
  echo "FAIL: cargo test loopback_login not ok"
  FX_ADDR=$ADDR timeout 300 cargo test loopback_login 2>&1 | tail -8
  exit 1
fi
