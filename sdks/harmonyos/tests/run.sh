#!/bin/sh
# HarmonyOS SDK wire tests without DevEco Studio: compile the real
# Etunnel.ets with tsc against the build-time stub types/ohos-stub.d.ts (the
# same one scripts/pack-harmonyos.sh uses), provide the two `@ohos.*` modules
# at run time by the Node stubs in tests/stubs (on top of Node's global
# WebSocket), and drive the SDK against the Go fixture server
# sdks/c/testtools/wssserver.
#
#   sh sdks/harmonyos/tests/run.sh
#
# Needs: node >= 22 (global WebSocket), tsc (npm i -g typescript), go.
set -eu
cd "$(dirname "$0")/.."
NODE_MAJOR=$(node -e 'console.log(process.versions.node.split(".")[0])')
[ "$NODE_MAJOR" -ge 22 ] || { echo "FAIL: node >= 22 required (global WebSocket); have $(node --version)"; exit 1; }
command -v tsc >/dev/null 2>&1 || { echo "FAIL: tsc not found (npm i -g typescript)"; exit 1; }

V=$(sed -n "s/^export const VERSION = '\(.*\)';$/\1/p" Etunnel.ets)
echo "sdk version: $V (VERSION file: $(cat ../../VERSION))"
[ "$V" = "$(cat ../../VERSION)" ] || { echo "FAIL: VERSION constant != VERSION file"; exit 1; }

T=$(mktemp -d)
mkdir -p "$T/out" "$T/node_modules/@ohos.net.websocket" "$T/node_modules/@ohos.net.http"
cp tests/stubs/@ohos.net.websocket/index.js "$T/node_modules/@ohos.net.websocket/"
cp tests/stubs/@ohos.net.http/index.js "$T/node_modules/@ohos.net.http/"
cp Etunnel.ets "$T/Etunnel.ts"
cp types/ohos-stub.d.ts "$T/ohos-stub.d.ts"

echo "== compile Etunnel.ets (strict, types/ohos-stub.d.ts) =="
# node16 module resolution keeps CommonJS output (no package.json "type" in
# the temp dir) and is accepted by TypeScript 6, where "node" (node10) is not.
tsc --strict --target ES2020 --module node16 --moduleResolution node16 --lib ES2020,DOM \
    --outDir "$T/out" "$T/Etunnel.ts" "$T/ohos-stub.d.ts"
[ -f "$T/out/Etunnel.js" ] || { echo "FAIL: tsc produced no out/Etunnel.js"; find "$T/out" -type f; exit 1; }

echo "== build fixture (sdks/c/testtools/wssserver) =="
(cd ../.. && go build -o "$T/wss" ./sdks/c/testtools/wssserver)

echo "== run =="
SDK_JS="$T/out/Etunnel.js" SDK_VERSION="$V" FIXTURE_BIN="$T/wss" node tests/hello.test.js
