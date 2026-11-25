#!/usr/bin/env bash
# Build the HarmonyOS HAR for sdks/harmonyos without DevEco Studio.
#
# Why not hvigor: `assembleHar` needs a hvigor *project* shell plus the OHOS SDK
# (ets-loader), which only ships with DevEco Studio / the login-gated
# "Command Line Tools" download. The SDK is a single ArkTS file with no UI and no
# native code, so the HAR is produced the way hvigor would emit it for such a
# module - ESM JavaScript + .d.ts compiled by tsc from Etunnel.ets, laid out
# exactly like a published HAR (reference: @ohos/axios 2.2.15 from ohpm):
#
#   package/oh-package.json5   main=index.js types=index.d.ts type=module
#   package/index.js           export * from './src/main/ets/Etunnel.js'
#   package/index.d.ts         export * from './src/main/ets/Etunnel'
#   package/src/main/ets/Etunnel.{js,d.ts}
#   package/src/main/module.json   {"module":{"name":"etunnel","type":"har",...}}
#   package/build-profile.json5, hvigorfile.ts, LICENSE, README.md
#
# Output: dist/packages/etunnel-harmonyos-<version>.har (tgz with package/ root).
# Requires: node + npx (typescript is fetched by npx, pinned below), tar, openssl.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MOD="$ROOT/sdks/harmonyos"
OUT="$ROOT/dist/packages"
TSC_VERSION="${TSC_VERSION:-5.5.4}"   # pinned so CI and local builds agree
mkdir -p "$OUT"

# Version comes from oh-package.json5 (the manifest bump-release.py stamps).
V="$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]*)".*/\1/p' "$MOD/oh-package.json5" | head -1)"
[ -n "$V" ] || { echo "pack-harmonyos: cannot read version from oh-package.json5" >&2; exit 1; }
HAR="$OUT/etunnel-harmonyos-$V.har"
STAGE="$MOD/.build/har"
PKG="$STAGE/package"
echo "pack-harmonyos: etunnel $V"

# 1. Compile ArkTS source (as .ts) with the @ohos.* stub into ESM JS + d.ts.
rm -rf "$STAGE"; mkdir -p "$STAGE/src" "$PKG/src/main/ets"
cp "$MOD/Etunnel.ets" "$STAGE/src/Etunnel.ts"
( cd "$MOD" && npx -y -p "typescript@$TSC_VERSION" tsc -p tsconfig.har.json )
[ -f "$STAGE/tsc/Etunnel.js" ] && [ -f "$STAGE/tsc/Etunnel.d.ts" ] \
  || { echo "pack-harmonyos: tsc produced no output" >&2; exit 1; }
# The emitted module must keep the native SDK imports (resolved by the OHOS runtime).
grep -q "from '@ohos.net.websocket'" "$STAGE/tsc/Etunnel.js" \
  || { echo "pack-harmonyos: compiled JS lost its @ohos.* import" >&2; exit 1; }
cp "$STAGE/tsc/Etunnel.js" "$STAGE/tsc/Etunnel.d.ts" "$PKG/src/main/ets/"

# 2. Library entry points (same shape as hvigor's HAR output).
printf '%s\n' "export * from './src/main/ets/Etunnel.js';" > "$PKG/index.js"
printf '%s\n' "export * from './src/main/ets/Etunnel';" > "$PKG/index.d.ts"

# 3. oh-package.json5: the module manifest, rewritten for a compiled HAR.
node - "$MOD/oh-package.json5" "$PKG/oh-package.json5" "$V" <<'EOF'
const [src, dst, v] = process.argv.slice(2);
const m = JSON.parse(require('fs').readFileSync(src, 'utf8')); // file is strict JSON5 == JSON here
const out = {
  name: m.name, version: v, description: m.description, main: 'index.js', types: 'index.d.ts',
  type: 'module', license: m.license, homepage: m.homepage, repository: m.repository,
  author: m.author, keywords: m.keywords, tags: ['Network'], dependencies: m.dependencies || {},
  devDependencies: m.devDependencies || {},
};
if (out.version !== m.version) { console.error('pack-harmonyos: version drift'); process.exit(1); }
require('fs').writeFileSync(dst, JSON.stringify(out));
EOF

# 4. src/main/module.json (hvigor emits JSON, not JSON5, inside the HAR).
cat > "$PKG/src/main/module.json" <<EOF
{
  "app": {
    "bundleName": "dev.ggcode.etunnel",
    "versionCode": 1,
    "versionName": "$V",
    "minAPIVersion": 12,
    "targetAPIVersion": 12,
    "apiReleaseType": "Release",
    "compileSdkVersion": "5.0.0(12)",
    "compileSdkType": "HarmonyOS",
    "bundleType": "app"
  },
  "module": {
    "name": "etunnel",
    "type": "har",
    "deviceTypes": ["phone", "tablet", "2in1"],
    "installationFree": false
  }
}
EOF

# 5. Static files every published HAR carries.
cp "$MOD/build-profile.json5" "$MOD/hvigorfile.ts" "$MOD/README.md" "$PKG/"
cp "$ROOT/LICENSE" "$PKG/LICENSE"

# 6. Tarball with the `package/` root every ohpm HAR uses.
rm -f "$HAR"
tar -czf "$HAR" -C "$STAGE" package
echo "pack-harmonyos: wrote $HAR ($(wc -c < "$HAR" | tr -d ' ') bytes)"
echo "pack-harmonyos: sha256 $(openssl dgst -sha256 "$HAR" | awk '{print $2}')"
