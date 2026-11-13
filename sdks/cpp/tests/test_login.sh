#!/bin/sh
# E2E: CLI loopback login (sdks/cpp). Runs natively when the host toolchain is
# websocketpp-compatible (CI ubuntu); on hosts where brew boost >= 1.92 broke
# asio::io_service it runs the FULL flow (compile + fixture + login) inside a
# linux container against the same repo checkout — that counts as a real run.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/cpp"

try_native() {
    command -v g++ >/dev/null 2>&1 || return 1
    printf '#include <websocketpp/config/asio_client.hpp>\n' | g++ -fsyntax-only -x c++ -std=c++17 - >/dev/null 2>&1
}

cat > /tmp/et_login_main.cpp <<'CEOF'
#include <cstdio>
#include <cstdlib>
#include <string>
#include <vector>
#include <functional>
struct et_credentials { std::string api_token; std::vector<std::string> subdomains; std::string plan; };
et_credentials et_login(const std::string &server, std::function<void(const std::string &)> open_browser = {}, long timeout_secs = 600);
int main(int argc, char **argv) {
    if (argc < 2) return 2;
    auto c = et_login(argv[1], [](const std::string &u) {
        std::string cmd = "curl -s -o /dev/null '" + u + "' >/dev/null 2>&1 &";
        (void)std::system(cmd.c_str());
    }, 30);
    std::string subs;
    for (size_t i = 0; i < c.subdomains.size(); i++) subs += (i ? ":" : "") + c.subdomains[i];
    std::printf("CRED:%s:%s:%s\n", c.api_token.c_str(), subs.c_str(), c.plan.c_str());
    return 0;
}
CEOF

start_fixture() {
    (cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
    OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
    FX=$!
    for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
    ADDR=$(grep listening "$OUT" | awk '{print $2}')
    [ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }
}
FX=""
trap 'kill $FX 2>/dev/null || true' EXIT

if try_native; then
    start_fixture
    g++ -o /tmp/et_cpp_login etunnel.cpp /tmp/et_login_main.cpp -std=c++17 -lcurl -lssl -lcrypto
    RESULT=$(/tmp/et_cpp_login "http://$ADDR" 2>&1) || RESULT="ERR:$RESULT"
else
    # Container path: cross-compile the fixture for the container's arch,
    # mount repo + fixture into ubuntu:24.04, compile there, run there —
    # fixture and login share the container's 127.0.0.1, so the loopback
    # flow is exercised for real.
    command -v docker >/dev/null 2>&1 || { echo "SKIP: no compatible native toolchain and no docker"; exit 0; }
    ARCH=$(docker info --format '{{.Architecture}}' 2>/dev/null || echo arm64)
    [ "$ARCH" = "aarch64" ] && ARCH=arm64
    [ "$ARCH" = "x86_64" ] && ARCH=amd64
    (cd "$REPO" && GOOS=linux GOARCH=${ARCH} go build -o /tmp/et-loginserver-linux ./sdks/testtools/loginserver)
    RESULT=$(docker run --rm -v "$REPO/sdks:/w" -w /w -v /tmp/et-loginserver-linux:/fx -v /tmp/et_login_main.cpp:/main.cpp ubuntu:24.04 bash -c '
        set -e
        apt-get update -qq >/dev/null 2>&1
        apt-get install -y -qq --no-install-recommends g++ libwebsocketpp-dev libboost-system-dev libcurl4-openssl-dev libssl-dev curl >/dev/null 2>&1
        # etunnel.cpp carries the CLI main; compile it with main renamed (this
        # translation unit only) so the harness owns the entrypoint.
        g++ -c -o /tmp/lib.o -Dmain=cli_main cpp/etunnel.cpp -std=c++17 || exit 1
        g++ -c -o /tmp/main.o /main.cpp -std=c++17 || exit 1
        g++ -o /tmp/login /tmp/lib.o /tmp/main.o -lcurl -lssl -lcrypto || exit 1
        (/fx > /tmp/fxlog 2>/dev/null &)
        for i in 1 2 3 4 5 6 7 8 9 10; do grep -q listening /tmp/fxlog 2>/dev/null && break; sleep 1; done
        addr=$(grep listening /tmp/fxlog | awk "{print \$2}")
        [ -n "$addr" ] || { echo FAIL-fixture; exit 1; }
        timeout 60 /tmp/login "http://$addr"
    ' 2>&1 | tail -1) || RESULT="ERR:$RESULT"
fi

case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
