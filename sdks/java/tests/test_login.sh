#!/bin/sh
# E2E: CLI loopback login (sdks/java) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/java"

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

cat > /tmp/LoginCheck.java <<'EOF'
package dev.ggcode;
public class LoginCheck {
    public static void main(String[] a) throws Exception {
        Etunnel.LoginResult c = Etunnel.login("http://" + System.getenv("FX_ADDR"),
            u -> { Runtime.getRuntime().exec(new String[]{"curl", "-s", "-o", "/dev/null", u}); }, 30);
        System.out.println("CRED:" + c.apiToken + ":" + c.subdomains + ":" + c.plan);
    }
}
EOF
javac -d /tmp/lcj src/main/java/dev/ggcode/Etunnel.java /tmp/LoginCheck.java
RESULT=$(FX_ADDR=$ADDR timeout 60 java -cp /tmp/lcj dev.ggcode.LoginCheck 2>&1) || RESULT="ERR:$RESULT"

case "$RESULT" in
  "CRED:tok-123:[my-sub]:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
