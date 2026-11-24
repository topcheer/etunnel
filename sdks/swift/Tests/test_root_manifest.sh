#!/bin/sh
# Root Package.swift regression (docs audit, P1): SPM consumers resolve the
# repository ROOT (.package(url: "https://github.com/topcheer/etunnel.git")),
# which used to be one executable target over the whole sdks/swift tree with
# no library product -> consumers got
#   sdks/swift/etunnel-cli/main.swift:40:1: error: statements are not allowed at the top level
# and `import etunnel` did not exist. This builds a throw-away consumer package
# that depends on this checkout by path and uses the library, then runs it.
# ROOT_MANIFEST_DIR=<another checkout> tests that checkout's root manifest.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
ROOT=${ROOT_MANIFEST_DIR:-$REPO}
NAME=$(basename "$ROOT")   # SwiftPM derives a path dependency's identity from the directory name
command -v swift >/dev/null 2>&1 || { echo "SKIP: swift not installed"; exit 77; }
[ "$(uname -s)" = "Darwin" ] || { echo "SKIP: URLSessionWebSocketTask is Apple-only (#90)"; exit 77; }

T=$(mktemp -d /tmp/et-spm-consumer.XXXXXX)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/Sources/consumer"
cat > "$T/Package.swift" <<EOF
// swift-tools-version:5.9
import PackageDescription
let package = Package(
    name: "consumer",
    platforms: [.macOS(.v13)],
    dependencies: [.package(path: "$ROOT")],
    targets: [.executableTarget(name: "consumer", dependencies: [.product(name: "etunnel", package: "$NAME")])]
)
EOF
cat > "$T/Sources/consumer/main.swift" <<'EOF'
import Foundation
import etunnel
// README "Minimal embed": the library API a consumer actually uses
let t = Etunnel(server: "wss://t.etun.uk/tunnel", local: "3000")
print(t == nil ? "nil" : "etunnel library linked")
EOF
cd "$T"
set +e
OUT=$(swift build 2>&1); RC=$?
set -e
echo "$OUT" | grep -E ': error:' | head -3 || true
[ "$RC" -eq 0 ] || { echo "FAIL: consumer swift build exit=$RC"; exit 1; }
./.build/debug/consumer | grep -q "etunnel library linked" || { echo "FAIL: consumer binary did not run"; exit 1; }
echo "PASS: root Package.swift exposes library product 'etunnel' to SPM consumers"
