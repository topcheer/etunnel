#!/bin/sh
# Build the Maven Central artifacts of the Kotlin and Scala SDKs:
#   dev.ggcode:etunnel-kotlin            (sdks/kotlin/Etunnel.kt)
#   dev.ggcode:etunnel_2.13               (sdks/scala/Etunnel.scala, Scala 2.13)
#   dev.ggcode:etunnel_3                  (sdks/scala/Etunnel.scala, Scala 3)
# For each coordinate: <artifact>-<V>.jar, -sources.jar, -javadoc.jar in the
# SDK's target/central/ directory, where the sibling pom.xml / pom-2.13.xml /
# pom-3.xml pick them up (build-helper attach-artifact) so that
# `mvn -Pcentral -f <pom> deploy` signs (gpg) and publishes the full set.
# `make pack-kotlin` / `make pack-scala` copy the set into dist/packages/maven.
#
# Usage: build-central-kotlin-scala.sh [kotlin|scala-2.13|scala-3|all]   (default all)
# Toolchain (auto-detected, override via env):
#   KOTLINC          kotlinc (needs -language-version 2.1 support: any 2.1+ compiler)
#   KOTLIN_STDLIB    kotlin-stdlib jar used at compile time (default: 2.1.0 from Maven Central)
#   SCALAC_2_13      scalac 2.13 (CI: SDKMAN 2.13.14)        SCALA_2_13_VERSION (pom dep)
#   SCALAC_3         scalac 3.x   (CI: SDKMAN 3.3.x LTS)     SCALA_3_VERSION    (pom dep)
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
V=$(cat "$ROOT/VERSION")
WHAT=${1:-all}
KOTLINC=${KOTLINC:-$(command -v kotlinc || true)}
KOTLIN_STDLIB=${KOTLIN_STDLIB:-}
SCALAC_2_13=${SCALAC_2_13:-$(command -v scalac-2.13 || ls /opt/homebrew/opt/scala@2.13/bin/scalac 2>/dev/null || true)}
SCALA_2_13_VERSION=${SCALA_2_13_VERSION:-2.13.14}
SCALAC_3=${SCALAC_3:-$(command -v scalac || true)}
SCALA_3_VERSION=${SCALA_3_VERSION:-3.3.9}
TMP=$(mktemp -d /tmp/et-central-build.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing tool: $1 ($2)"; exit 1; }; }
need jar "JDK"

# javadoc jar: Kotlin/Scala have no javadoc tool output; Central requires a
# non-empty javadoc jar with HTML. Ship the javadoc layout (index.html +
# element-list/package-list + package-summary) with the API summary instead.
write_javadoc() { # $1 = dir, $2 = title, $3 = coordinates, $4 = language
    mkdir -p "$1/dev/ggcode/etunnel"
    cat > "$1/index.html" <<HTML
<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>$2 API</title></head>
<body><h1>$2</h1>
<p>Maven Central artifact <code>$3</code>. $4 SDK for <a href="https://github.com/topcheer/etunnel">etunnel</a>:
expose a local service on the public internet over one outbound WebSocket. Package
<code>dev.ggcode.etunnel</code> - see <a href="dev/ggcode/etunnel/package-summary.html">package-summary</a>
and the README at <a href="https://github.com/topcheer/etunnel/tree/main/sdks/$5">sdks/$5</a>.</p>
</body></html>
HTML
    printf 'dev.ggcode.etunnel\n' > "$1/element-list"
    printf 'dev.ggcode.etunnel\n' > "$1/package-list"
    cat > "$1/dev/ggcode/etunnel/package-summary.html" <<HTML
<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>dev.ggcode.etunnel</title></head>
<body><h2>Package <code>dev.ggcode.etunnel</code></h2>
<p>Buffer-based protocol client (JDK 11+ <code>java.net.http</code>, no dependencies beyond the $4
standard library). Main entry points:</p>
<ul>
<li><code>connect(server, local[, token, subdomain, ...])</code> - open a tunnel; returns the tunnel object (<code>url</code>, <code>resumeToken</code>, <code>close()</code>).</li>
<li><code>Route(prefix, addr, strip)</code>, <code>normalizeRoutes</code>, <code>routeFor</code> - client-side multi-target routing (PROTOCOL.md section 2).</li>
<li><code>login(server, ...)</code> - loopback browser login (paid plans).</li>
</ul>
<p>Wire protocol: <a href="https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md">PROTOCOL.md</a>.</p>
</body></html>
HTML
}

jar_of() { # $1 = classes dir, $2 = output jar, $3 = label
    rm -f "$2"
    jar cf "$2" -C "$1" .
    echo "== $3: $(basename "$2") =="
    jar tf "$2" | grep -v '/$' | grep -v '^META-INF/MANIFEST.MF$' | sort | sed 's/^/   /'
    if jar tf "$2" | grep -qE '^(kotlin|scala|org/scala-lang|org/jetbrains)/'; then
        echo "FAIL: $2 bundles a standard library (must not)"; exit 1
    fi
    jar tf "$2" | grep -q '^dev/ggcode/etunnel/' || { echo "FAIL: $2 has no dev/ggcode/etunnel classes"; exit 1; }
}

sources_jar() { # $1 = source file, $2 = output jar, $3 = label
    mkdir -p "$TMP/src/dev/ggcode/etunnel" && cp "$1" "$TMP/src/dev/ggcode/etunnel/"
    rm -f "$2" && jar cf "$2" -C "$TMP/src" . && rm -rf "$TMP/src"
    echo "== $3 sources: $(jar tf "$2" | grep -v '/$' | grep -v META-INF | tr '\n' ' ')"
}

javadoc_jar() { # $1 = output jar, $2 = title, $3 = coordinates, $4 = language, $5 = sdk dir
    rm -rf "$TMP/jd"; write_javadoc "$TMP/jd" "$2" "$3" "$4" "$5"
    rm -f "$1" && jar cf "$1" -C "$TMP/jd" .
    echo "== $2 javadoc: $(jar tf "$1" | grep -v '/$' | grep -v META-INF | tr '\n' ' ')"
}

if [ "$WHAT" = all ] || [ "$WHAT" = kotlin ]; then
    [ -n "$KOTLINC" ] || { echo "kotlinc not found (set KOTLINC=...)"; exit 1; }
    if [ -z "$KOTLIN_STDLIB" ]; then
        KOTLIN_STDLIB="$TMP/kotlin-stdlib-2.1.0.jar"
        curl -fsSL -o "$KOTLIN_STDLIB" https://repo1.maven.org/maven2/org/jetbrains/kotlin/kotlin-stdlib/2.1.0/kotlin-stdlib-2.1.0.jar
    fi
    OUT="$ROOT/sdks/kotlin/target/central"; mkdir -p "$OUT" "$TMP/kclasses"
    cd "$ROOT/sdks/kotlin"
    # Library jar (no -include-runtime): consumers get kotlin-stdlib from the pom.
    # Compiled against stdlib 2.1.0 with language/api 2.1 so Kotlin 2.1 projects
    # can consume it (the Android SDK pins the same stdlib); JVM target 11 like
    # the Java SDK (maven.compiler.release=11).
    "$KOTLINC" -language-version 2.1 -api-version 2.1 -jvm-target 11 -no-stdlib -cp "$KOTLIN_STDLIB" -d "$TMP/kclasses" Etunnel.kt 2>&1 | grep -v "^warning:" || true
    jar_of "$TMP/kclasses" "$OUT/etunnel-kotlin-$V.jar" "etunnel-kotlin"
    sources_jar Etunnel.kt "$OUT/etunnel-kotlin-$V-sources.jar" etunnel-kotlin
    javadoc_jar "$OUT/etunnel-kotlin-$V-javadoc.jar" etunnel-kotlin "dev.ggcode:etunnel-kotlin" Kotlin kotlin
    echo "   metadata version: $(javap -v -cp "$TMP/kclasses" dev.ggcode.etunnel.Etunnel 2>/dev/null | grep -o 'mv=\[[0-9,]*\]' | head -1)"
fi

if [ "$WHAT" = all ] || [ "$WHAT" = scala-2.13 ]; then
    [ -n "$SCALAC_2_13" ] || { echo "scalac 2.13 not found (set SCALAC_2_13=...)"; exit 1; }
    OUT="$ROOT/sdks/scala/target/central"; mkdir -p "$OUT" "$TMP/sclasses213"
    cd "$ROOT/sdks/scala"
    "$SCALAC_2_13" -d "$TMP/sclasses213" Etunnel.scala 2>&1 | grep -i "error" || true
    jar_of "$TMP/sclasses213" "$OUT/etunnel_2.13-$V.jar" "etunnel_2.13 ($("$SCALAC_2_13" -version 2>&1 | head -1))"
    sources_jar Etunnel.scala "$OUT/etunnel_2.13-$V-sources.jar" etunnel_2.13
    javadoc_jar "$OUT/etunnel_2.13-$V-javadoc.jar" "etunnel_2.13" "dev.ggcode:etunnel_2.13" Scala scala
fi

if [ "$WHAT" = all ] || [ "$WHAT" = scala-3 ]; then
    [ -n "$SCALAC_3" ] || { echo "scalac 3 not found (set SCALAC_3=...)"; exit 1; }
    OUT="$ROOT/sdks/scala/target/central"; mkdir -p "$OUT" "$TMP/sclasses3"
    cd "$ROOT/sdks/scala"
    # Scala 3 TASTy is forward-compatible only within one minor version, so the
    # published _3 artifact is compiled with the 3.3 LTS line in CI (readable by
    # every 3.3+ compiler); the pom depends on the same scala3-library.
    "$SCALAC_3" -d "$TMP/sclasses3" Etunnel.scala 2>&1 | grep -i "error" || true
    jar_of "$TMP/sclasses3" "$OUT/etunnel_3-$V.jar" "etunnel_3 ($("$SCALAC_3" -version 2>&1 | head -1))"
    sources_jar Etunnel.scala "$OUT/etunnel_3-$V-sources.jar" etunnel_3
    javadoc_jar "$OUT/etunnel_3-$V-javadoc.jar" "etunnel_3" "dev.ggcode:etunnel_3" Scala scala
fi
echo "central artifacts for $V built"
