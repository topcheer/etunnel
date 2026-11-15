#!/bin/sh
# #53 Swift regression: a malformed ETUNNEL_SERVER used to crash the process
# on URL(string:)!; it must exit gracefully with a message instead.
# Fixture (rust/testtools/wssserver) is shared across the #53 SDKs.
set -eu
cd "$(dirname "$0")/.."

swift build 2>&1 | grep -E '^error' | head -3 || true

set +e
OUT=$(ETUNNEL_SERVER="not a url at all" timeout 15 ./.build/debug/etunnel 39702 2>&1)
RC=$?
set -e
echo "exit=$RC out: $(echo "$OUT" | head -1)"
[ "$RC" -ne 0 ] || { echo "FAIL: exited 0 on a malformed server"; exit 1; }
echo "$OUT" | grep -qi 'invalid server' || { echo "FAIL: no graceful error message"; exit 1; }
# and it must not be a crash report
echo "$OUT" | grep -qi 'Fatal error\|precondition\|signal' && { echo "FAIL: crashed instead of graceful error"; exit 1; }
echo "PASS: swift_bad_url"
