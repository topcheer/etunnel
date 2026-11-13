#!/bin/sh
# E2E: CLI loopback login (sdks/kotlin) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/kotlin"

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

cat > /tmp/LoginCheckKt.kt <<'EOF'
fun main() {
    val c = login("http://" + System.getenv("FX_ADDR"),
        BrowserOpener { u -> ProcessBuilder("curl", "-s", "-o", "/dev/null", u).start() }, 30)
    println("CRED:" + c.apiToken + ":" + c.subdomains + ":" + c.plan)
}
EOF
# login() 依赖同文件 Json/parser —— 与 Etunnel.kt 同 default 包，一并编译
timeout 240 kotlinc Etunnel.kt /tmp/LoginCheckKt.kt -include-runtime -d /tmp/lck.jar 2>/dev/null
RESULT=$(FX_ADDR=$ADDR timeout 60 java -cp /tmp/lck.jar LoginCheckKtKt 2>&1) || RESULT="ERR:$RESULT"

case "$RESULT" in
  "CRED:tok-123:[my-sub]:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
