#!/bin/sh
# #183 E2E: the Swift supervisor survives a server restart and reclaims the
# SAME public URL via the resume token, and it publishes the state transitions
# a UI needs (retrying -> connected).
#
# Mirrors reconnect_integration_test.go on the Go side: the server is stopped
# hard (kill -9, like a process exit) and a fresh instance is brought up on the
# same port.
#
# Run from sdks/swift:  ./tests/test_reconnect.sh       (uses 8xxx ports)
set -eu
cd "$(dirname "$0")/.."
SWIFT_DIR=$(pwd)
REPO=$(cd "$SWIFT_DIR/../.." && pwd)
PORT=${ET_PORT:-18937}
ADMIN_PORT=$((PORT + 1))
DATA_DIR=$(mktemp -d)
TMPD=$(mktemp -d)
SLOG="$TMPD/server.log"
CLOG="$TMPD/client.log"

cleanup() {
  [ -n "${CLI_PID:-}" ] && kill -9 "$CLI_PID" 2>/dev/null || true
  [ -n "${SRV_PID:-}" ] && kill -9 "$SRV_PID" 2>/dev/null || true
  rm -rf "$DATA_DIR" "$TMPD"
}
trap cleanup EXIT INT TERM

say() { echo "[reconnect-e2e] $*"; }
fail() { echo "FAIL: $*"; echo "--- server log ---"; tail -20 "$SLOG" 2>/dev/null || true
         echo "--- client log ---"; tail -20 "$CLOG" 2>/dev/null || true; exit 1; }

say "building etunneld"
( cd "$REPO" && go build -o "$TMPD/etunneld" ./cmd/etunneld ) || fail "go build etunneld"
say "building swift CLI"
swift build 2>&1 | grep -E '^error' && fail "swift build"
CLI="$SWIFT_DIR/.build/debug/etunnel-cli"
[ -x "$CLI" ] || fail "CLI binary missing at $CLI"

start_server() {
  # Resume tokens are HMAC'd with SESSION_SECRET; a restarted process without it
  # mints a new key and rejects older tokens (PROTOCOL.md). Production sets it;
  # so must this harness, otherwise it tests a scenario nobody runs.
  export SESSION_SECRET="e2e-session-secret-0123456789abcdef"
  "$TMPD/etunneld" \
    -domain 127.0.0.1 \
    -tls-mode http \
    -addr "127.0.0.1:$PORT" \
    -http-addr "" \
    -admin-addr "127.0.0.1:$ADMIN_PORT" \
    -admin-token e2e-token \
    -accounts "$DATA_DIR/accounts.json" \
    -ws-path /tunnel >>"$SLOG" 2>&1 &
  SRV_PID=$!
  # wait for the listener
  i=0
  while [ $i -lt 60 ]; do
    if curl -sf -o /dev/null "http://127.0.0.1:$ADMIN_PORT/healthz" 2>/dev/null; then return 0; fi
    if curl -sf -o /dev/null "http://127.0.0.1:$ADMIN_PORT/stats" -H "X-Admin-Token: e2e-token" 2>/dev/null; then return 0; fi
    i=$((i + 1)); sleep 0.2
  done
  fail "server did not come up on 127.0.0.1:$PORT"
}

say "starting server on :$PORT"
start_server

say "starting client"
ETUNNEL_SERVER="ws://127.0.0.1:$PORT/tunnel" \
  "$CLI" 39999 >"$CLOG" 2>&1 &
CLI_PID=$!

# first URL
i=0
URL1=""
while [ $i -lt 100 ]; do
  URL1=$(grep -m1 '^TUNNEL_URL: ' "$CLOG" 2>/dev/null | sed 's/^TUNNEL_URL: //') || true
  [ -n "$URL1" ] && break
  i=$((i + 1)); sleep 0.2
done
[ -n "$URL1" ] || fail "client never reported a TUNNEL_URL"
say "connected: $URL1"

say "hard-killing the server (simulating a restart)"
kill -9 "$SRV_PID" 2>/dev/null || true
wait "$SRV_PID" 2>/dev/null || true

# the client must notice and enter the retrying state
i=0
while [ $i -lt 150 ]; do
  grep -q 'connection lost, retry' "$CLOG" 2>/dev/null && break
  i=$((i + 1)); sleep 0.2
done
grep -q 'connection lost, retry' "$CLOG" || fail "client did not report a lost connection"
say "client observed the drop: $(grep -m1 'connection lost, retry' "$CLOG")"

say "restarting the server"
SRV_PID=""
start_server

# now it must reconnect on its own, with the SAME url
i=0
URL2=""
while [ $i -lt 300 ]; do
  URL2=$(grep '^TUNNEL_URL: ' "$CLOG" 2>/dev/null | sed -n '2p' | sed 's/^TUNNEL_URL: //') || true
  [ -n "$URL2" ] && break
  i=$((i + 1)); sleep 0.2
done
[ -n "$URL2" ] || fail "client never reconnected"
say "reconnected: $URL2"

grep -q 'reconnected after' "$CLOG" || fail "no reconnect log line"
[ "$URL1" = "$URL2" ] || fail "subdomain changed across reconnect: '$URL1' -> '$URL2'"

# A deliberate close must stop the supervisor: no reconnect chatter afterwards.
kill -TERM "$CLI_PID" 2>/dev/null || true
wait "$CLI_PID" 2>/dev/null || true
CLI_PID=""
sleep 0.5
grep -q 'stopped:' "$CLOG" || fail "client did not report a clean stop"

echo "PASS: swift reconnect e2e (same url across restart: $URL1)"
