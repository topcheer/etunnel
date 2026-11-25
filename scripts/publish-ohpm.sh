#!/usr/bin/env bash
# Publish a HarmonyOS HAR to the central ohpm registry (https://ohpm.openharmony.cn)
# over the registry's npm-protocol API - no ohpm CLI needed (the CLI is not on npm
# or the Huawei npm mirror and its standalone download is login-gated).
#
# Protocol (verified against the live registry, see PR body):
#   GET  $REGISTRY/<name>                  packument: 200 with versions{} | 404 {} when absent
#   PUT  $REGISTRY/<name>                  npm "publish" body with _attachments[<file>] (base64 tgz)
#   Authorization: Bearer <OHPM_TOKEN>     dry-run with an invalid token answers 401 (not 400)
#
# Usage: scripts/publish-ohpm.sh dist/packages/etunnel-harmonyos-<v>.har [--dry-run]
# Env:   OHPM_TOKEN (publish token stored as the repository secret of the same name)
# Exit:  0 published-or-already-published (or dry-run accepted) ; 1 on any other answer.
set -euo pipefail

HAR="${1:?usage: publish-ohpm.sh <etunnel-<v>.har> [--dry-run]}"
DRY=0; [ "${2:-}" = "--dry-run" ] && DRY=1
[ -f "$HAR" ] || { echo "publish-ohpm: no such HAR: $HAR" >&2; exit 1; }
REGISTRY="${OHPM_REGISTRY:-https://ohpm.openharmony.cn/ohpm}"

# Name/version come from the manifest inside the HAR, not from the file name.
MANIFEST="$(tar -xzOf "$HAR" package/oh-package.json5)"
read -r NAME VERSION DESC LICENSE HOMEPAGE REPO <<<"$(node -e '
const m = JSON.parse(process.argv[1]);
console.log([m.name, m.version, m.description || "", m.license || "", m.homepage || "", m.repository || ""].join(" "));
console.error(JSON.stringify({ keywords: m.keywords || [], dependencies: m.dependencies || {}, devDependencies: m.devDependencies || {} }));
' "$MANIFEST" 2>/tmp/ohpm-manifest-extra.$$)"
EXTRA="$(cat /tmp/ohpm-manifest-extra.$$)"; rm -f "/tmp/ohpm-manifest-extra.$$"
echo "publish-ohpm: $NAME@$VERSION ($HAR) -> $REGISTRY"

# Idempotent probe (#412 rule): only an explicit "does not exist" (npm-style 404)
# counts as unpublished; any other answer aborts instead of blind-publishing.
code="$(curl -s -m 30 -o /tmp/ohpm-packument.$$ -w '%{http_code}' "$REGISTRY/$NAME")"
case "$code" in
  200)
    if node -e 'const d=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));process.exit(d.versions&&d.versions[process.argv[2]]?0:1)' /tmp/ohpm-packument.$$ "$VERSION"; then
      echo "ohpm: $NAME@$VERSION already published, skipping"; rm -f /tmp/ohpm-packument.$$; exit 0
    fi ;;
  404) echo "ohpm: $NAME not on the registry ($code) - publishing" ;;
  *) echo "::error::ohpm probe returned HTTP $code for $NAME - refusing to blind-publish"; rm -f /tmp/ohpm-packument.$$; exit 1 ;;
esac
rm -f /tmp/ohpm-packument.$$ 2>/dev/null || true

# npm "publish" document: versions[v] metadata + the tgz as a base64 attachment.
PAYLOAD="$(mktemp /tmp/ohpm-publish.XXXXXX.json)"
node - "$HAR" "$NAME" "$VERSION" "$DESC" "$LICENSE" "$HOMEPAGE" "$REPO" "$EXTRA" "$PAYLOAD" <<'EOF'
const fs = require('fs'), crypto = require('crypto');
const [har, name, version, desc, lic, home, repo, extraJson, out] = process.argv.slice(2);
const extra = JSON.parse(extraJson);
const buf = fs.readFileSync(har);
const file = `${name}-${version}.har`;
const doc = {
  _id: name, name, description: desc, 'dist-tags': { latest: version }, versions: {
    [version]: {
      _id: `${name}@${version}`, name, version, main: 'index.js', types: 'index.d.ts', type: 'module',
      description: desc, license: lic, homepage: home, repository: repo, keywords: extra.keywords,
      dependencies: extra.dependencies, devDependencies: extra.devDependencies, tags: ['Network'],
      _nodeVersion: process.versions.node, _ohpmVersion: 'npm-protocol/1.0 (scripts/publish-ohpm.sh)',
      dist: { tarball: file, shasum: crypto.createHash('sha1').update(buf).digest('hex'),
              integrity: 'sha512-' + crypto.createHash('sha512').update(buf).digest('base64') },
    } },
  _attachments: { [file]: { content_type: 'application/octet-stream', data: buf.toString('base64'), length: buf.length } },
  readme: '',
};
fs.writeFileSync(out, JSON.stringify(doc));
EOF
[ -n "${OHPM_TOKEN:-}" ] || [ "$DRY" = 1 ] || { echo "publish-ohpm: OHPM_TOKEN is not set" >&2; rm -f "$PAYLOAD"; exit 1; }
AUTH="Bearer ${OHPM_TOKEN:-invalid-dry-run-token}"

put="$(curl -s -m 300 -o /tmp/ohpm-put.body -w '%{http_code}' -X PUT "$REGISTRY/$NAME" \
  -H "Authorization: $AUTH" -H 'Content-Type: application/json' \
  --data-binary "@$PAYLOAD")"
echo "publish-ohpm: PUT $REGISTRY/$NAME -> HTTP $put"
head -c 400 /tmp/ohpm-put.body; echo
rm -f "$PAYLOAD"
if [ "$DRY" = 1 ]; then
  # ohpm answers an invalid token with HTTP 400 {"error":"The authorization is invalid!"}
  # (not 401); auth is checked before the payload, so this only proves the
  # endpoint, method and auth scheme - the npm-protocol body is validated on
  # the first real publish.
  case "$put" in
    401|403) echo "publish-ohpm: dry-run OK - rejected at authentication ($put)"; exit 0 ;;
    400) if grep -q 'authorization is invalid' /tmp/ohpm-put.body 2>/dev/null; then
           echo "publish-ohpm: dry-run OK - rejected at authentication (400 authorization is invalid)"; exit 0; fi ;;
  esac
  echo "publish-ohpm: dry-run unexpected answer $put (expected an authorization rejection)"; exit 1
fi
case "$put" in 200|201) echo "ohpm: published $NAME@$VERSION"; exit 0 ;; esac
echo "::error::ohpm publish failed with HTTP $put"; exit 1
rm -f /tmp/ohpm-put.body
