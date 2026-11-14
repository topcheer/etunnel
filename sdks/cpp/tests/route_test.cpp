// Route table tests (same semantics table as sdks/node/route.js).
// Includes the SDK source with main() renamed so the router can be
// exercised without the websocket loop.
#define main etunnel_cli_main_unused
#include "../etunnel.cpp"
#undef main

#include <cstdio>

static int failures = 0;
static void ck(const char *name, const std::string &got, const std::string &want) {
    if (got != want) { failures++; std::printf("FAIL %s: got %s want %s\n", name, got.c_str(), want.c_str()); }
}

int main() {
    std::string addr, path;
    parse_routes("/api=127.0.0.1:8080;/api/v2=127.0.0.1:9;/=3000");
    ck("/apiv2 -> /", route_for(g_routes, "/apiv2", addr, path) ? addr : "", "3000");
    ck("/apiary -> /", route_for(g_routes, "/apiary", addr, path) ? addr : "", "3000");
    ck("/api exact", route_for(g_routes, "/api", addr, path) ? addr : "", "127.0.0.1:8080");
    ck("/api/u", route_for(g_routes, "/api/u", addr, path) ? addr : "", "127.0.0.1:8080");
    ck("longest wins", route_for(g_routes, "/api/v2/x?q", addr, path) ? addr : "", "127.0.0.1:9");

    parse_routes("/api=127.0.0.1:8080,strip;/=3000,strip");
    route_for(g_routes, "/api/u?x=1&y=2", addr, path);
    ck("strip keeps query", path, "/u?x=1&y=2");
    route_for(g_routes, "/api", addr, path);
    ck("exact strip -> /", path, "/");
    route_for(g_routes, "/app?c=1", addr, path);
    ck("strip root prefix = original", path, "/app?c=1");

    parse_routes("/api=127.0.0.1:8080");
    ck("no match -> false", route_for(g_routes, "/other", addr, path) ? "hit" : "nil", "nil");

    if (failures == 0) { std::printf("cpp route tests: PASS\n"); return 0; }
    std::printf("cpp route tests: FAIL (%d)\n", failures);
    return 1;
}
