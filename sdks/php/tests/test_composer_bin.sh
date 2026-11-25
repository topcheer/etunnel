#!/bin/sh
# #430 (part 1) regression: `composer require ggcode/etunnel` then
# `php vendor/bin/etunnel.php 3000` used to exit silently (status 0, no
# TUNNEL_URL). Composer's bin proxy includes sdks/php/etunnel.php with
# $argv[0] = vendor/bin/etunnel.php, so the CLI guard's
# `realpath($argv[0]) === __FILE__` was never true. The guard now also
# recognises the proxy via $GLOBALS['_composer_bin_dir'].
#
# Installs THIS checkout through a composer `path` repository (offline; no
# Packagist) into a throw-away project and checks both directions:
#   1. php vendor/bin/etunnel.php 3000 -> TUNNEL_URL, tunnel probes
#   2. a program that only autoloads the package never starts the CLI
#
# Run (harness up):  sh sdks/php/tests/test_composer_bin.sh
#   ET_COMPOSER="php /path/composer.phar"  overrides the composer command
#     (not COMPOSER: that is composer's own env var for the composer.json path)
#   ET_PHP_REPO_DIR=<other checkout>     tests that checkout (fail-before)
# Exit 77 = composer missing / harness not reachable.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=${ET_PHP_REPO_DIR:-$(cd "$HERE/../../.." && pwd)}
ET_COMPOSER=${ET_COMPOSER:-composer}
if [ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' -H 'Host: localhost' http://127.0.0.1:8080/)" != 200 ]; then
    echo "SKIP: harness not reachable (/tmp/et-harness.sh up)"; exit 77
fi
$ET_COMPOSER --version >/dev/null 2>&1 || { echo "SKIP: composer not available (set COMPOSER=...)"; exit 77; }

T=$(mktemp -d /tmp/et-composer-bin.XXXXXX)
# Stage a minimal package (root composer.json + sdks/php/etunnel.php) instead of
# pointing the path repository at the checkout: composer chmods bin sources
# 0755 on install, which would show up as a mode change in the real tree.
mkdir -p "$T/pkg/sdks/php"
cp "$REPO/composer.json" "$T/pkg/"
cp "$REPO/sdks/php/etunnel.php" "$T/pkg/sdks/php/"
PHP=""
# composer path repositories symlink the package into vendor/; rm -rf does not
# follow symlinks, so only the throw-away project is removed.
cleanup() { kill $PHP 2>/dev/null || true; rm -rf "$T"; }
trap cleanup EXIT

cat > "$T/composer.json" <<EOF
{
  "name": "acme/consumer",
  "repositories": [{"type": "path", "url": "$T/pkg"}],
  "require": {"ggcode/etunnel": "*"}
}
EOF
( cd "$T" && $ET_COMPOSER install --no-interaction --no-progress --quiet 2>&1 | tail -3 ) || { echo "FAIL: composer install failed"; exit 1; }
[ -f "$T/vendor/bin/etunnel.php" ] || { echo "FAIL: vendor/bin/etunnel.php was not created"; ls "$T/vendor/bin" 2>/dev/null; exit 1; }

echo "== 1. vendor/bin/etunnel.php 3000 =="
OUT=$(mktemp)
ETUNNEL_SERVER=ws://localhost:8080/tunnel ETUNNEL_LOCAL=3000 php "$T/vendor/bin/etunnel.php" 3000 > "$OUT" 2>&1 &
PHP=$!
for _ in $(seq 1 60); do grep -q TUNNEL_URL "$OUT" 2>/dev/null && break; kill -0 $PHP 2>/dev/null || break; sleep 0.25; done
URL=$(grep -o 'http://[a-z0-9-]*\.localhost' "$OUT" | head -1)
if [ -z "$URL" ]; then
    echo "FAIL: vendor/bin/etunnel.php produced no TUNNEL_URL (#430); output:"; cat "$OUT"; exit 1
fi
HOST=${URL#http://}
echo "tunnel: $URL"
BODY=$(curl -s -m 10 -H "Host: $HOST" http://127.0.0.1:8080/ 2>/dev/null || true)
echo "$BODY" | grep -q "ET-LOCAL-OK" || { echo "FAIL: probe through the bin tunnel: $BODY"; exit 1; }
echo "probe through the bin tunnel: OK ($(echo "$BODY" | head -c 40))"
kill $PHP 2>/dev/null; wait $PHP 2>/dev/null || true; PHP=""

echo "== 2. autoload only must not start the CLI =="
cat > "$T/app.php" <<'EOF'
<?php
require __DIR__ . '/vendor/autoload.php';
echo class_exists('Etunnel') ? "app ran\n" : "app ran (no Etunnel class)\n";
EOF
APP=$(cd "$T" && ETUNNEL_SERVER=ws://localhost:8080/tunnel ETUNNEL_LOCAL=3000 timeout 5 php app.php 2>&1); RC=$?
echo "$APP" | tail -1
[ "$RC" -eq 0 ] || { echo "FAIL: autoload-only app exit $RC: $APP"; exit 1; }
echo "$APP" | grep -q "^app ran$" || { echo "FAIL: autoload-only app did not run cleanly: $APP"; exit 1; }
echo "$APP" | grep -q "TUNNEL_URL" && { echo "FAIL: autoload-only app started the CLI"; exit 1; }
echo "PASS: composer bin proxy runs the CLI; autoload alone does not (#430)"
