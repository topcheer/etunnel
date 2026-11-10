#!/bin/sh
# #54 regression ②: with ETUNNEL_RECONNECT=0 the supervisor's infinite delay
# did not carry the stop token, so no signal could ever terminate the process.
# Verified via SIGTERM, which feeds the exact same stop.Cancel() path as the
# SIGINT registration.
#
# Note on SIGINT: this sandbox cannot exercise it end-to-end - CoreCLR swallows
# SIGINT for processes without a controlling TTY even when a
# PosixSignalRegistration exists (minimal probe in the PR body). The code keeps
# the POSIX SIGINT registration for non-TTY contexts where CancelKeyPress
# never fires; SIGTERM proves the cancellation path itself.
set -eu
cd "$(dirname "$0")/.."

go build -o /tmp/wssc-cs ./testtools/wssserver
ADDR_FILE=$(mktemp)
( /tmp/wssc-cs -mode welcome -plain >"$ADDR_FILE" 2>/dev/null ) &
FX=$!
trap 'kill $FX 2>/dev/null || true' EXIT
for _ in $(seq 20); do [ -s "$ADDR_FILE" ] && break; sleep 0.5; done
ADDR=$(head -1 "$ADDR_FILE")

# `dotnet <dll>` hosts the app in the same process, so the signal reaches our
# handler directly.
DLL=bin/Debug/net10.0/Etunnel.dll
[ -f "$DLL" ] || { echo "FAIL: build output missing at $DLL"; exit 1; }
ETUNNEL_SERVER="ws://$ADDR/tunnel" ETUNNEL_RECONNECT=0 \
    ETUNNEL_LOCAL=39502 dotnet "$DLL" > /tmp/cs-oneshot.log 2>&1 &
CLI=$!
sleep 4
if ! grep -q TUNNEL_URL /tmp/cs-oneshot.log; then
    kill $CLI 2>/dev/null || true
    echo "FAIL: client did not connect"; exit 1
fi
kill -TERM $CLI
for _ in $(seq 20); do
    kill -0 $CLI 2>/dev/null || break
    sleep 0.5
done
if kill -0 $CLI 2>/dev/null; then
    echo "FAIL: SIGTERM did not terminate the one-shot process"
    kill -9 $CLI 2>/dev/null || true
    exit 1
fi
wait $CLI 2>/dev/null; RC=$?
echo "exited with $RC"
[ "$RC" -eq 0 ] || { echo "FAIL: non-zero exit"; exit 1; }
echo "PASS: oneshot_signal"
