#!/bin/sh
# E2E: CLI loopback login (sdks/cpp). Requires a websocketpp-compatible
# toolchain (upstream CI: ubuntu-22.04 + apt boost). On hosts where brew
# boost dropped asio::io_service this prints SKIP (compile-only there).
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/cpp"

printf '#include <websocketpp/config/asio_client.hpp>\n' | g++ -fsyntax-only -x c++ -std=c++17 - >/dev/null 2>&1 || {
  echo "SKIP: host toolchain incompatible with websocketpp (brew boost >= 1.92); runs on CI ubuntu"
  exit 0
}

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }

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
g++ -o /tmp/et_cpp_login etunnel.cpp /tmp/et_login_main.cpp -std=c++17 -lcurl
RESULT=$(/tmp/et_cpp_login "http://$ADDR" 2>&1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
