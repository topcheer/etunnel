#!/bin/sh
# E2E: CLI loopback login (sdks/c) against the shared loginserver fixture.
# Compiles a tiny harness alongside etunnel.c (et_login is a library fn).
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/c"
(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

cat > /tmp/et_login_main.c <<'CEOF'
#include <stdio.h>
#include <stdlib.h>
#include <stddef.h>
struct et_credentials {
    char api_token[256];
    char **subdomains;
    size_t n_subdomains;
    char plan[64];
};
struct et_credentials *et_login(const char *server, void (*open_browser)(const char *), long timeout_secs);
void et_login_free(struct et_credentials *c);
static void opener(const char *u) {
    char cmd[2048];
    snprintf(cmd, sizeof cmd, "curl -s -o /dev/null '%s' >/dev/null 2>&1 &", u);
    (void)system(cmd);
}
int main(int argc, char **argv) {
    if (argc < 2) return 2;
    struct et_credentials *c = et_login(argv[1], opener, 30);
    if (!c) { puts("CRED:FAIL"); return 1; }
    printf("CRED:%s:", c->api_token);
    for (size_t i = 0; i < c->n_subdomains; i++) printf("%s%s", i ? ":" : "", c->subdomains[i]);
    printf(":%s\n", c->plan);
    et_login_free(c);
    return 0;
}
CEOF
INC=""
for d in /opt/homebrew/opt/libwebsockets/include /opt/homebrew/opt/json-c/include; do [ -d "$d" ] && INC="$INC -I$d"; done
for s in /opt/homebrew/opt/openssl@3/include /opt/homebrew/opt/openssl/include; do [ -d "$s" ] && { INC="$INC -I$s"; break; }; done
LIBS=""
for d in /opt/homebrew/opt/libwebsockets/lib /opt/homebrew/opt/json-c/lib /opt/homebrew/opt/curl/lib; do [ -d "$d" ] && LIBS="$LIBS -L$d"; done
# Step 1: compile etunnel.c with its CLI main renamed away (-D applies to
# this translation unit only); step 2: the harness owns main.
gcc -c -o /tmp/et_lib.o -Dmain=cli_main etunnel.c $INC
gcc -c -o /tmp/et_main.o /tmp/et_login_main.c $INC
gcc -o /tmp/et_c_login /tmp/et_lib.o /tmp/et_main.o $LIBS -lwebsockets -lcurl -ljson-c
RESULT=$(/tmp/et_c_login "http://$ADDR" 2>&1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
