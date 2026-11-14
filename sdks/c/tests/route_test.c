/* Route table tests (same semantics table as sdks/node/route.js).
 * Includes the SDK source with main() renamed so the router can be
 * exercised without the libwebsockets loop. */
#define main etunnel_cli_main_unused
#include "../etunnel.c"
#undef main

static int failures = 0;
static void ck(const char *name, const char *got, const char *want) {
    if (strcmp(got, want) != 0) {
        failures++;
        printf("FAIL %s: got %s want %s\n", name, got, want);
    }
}

int main(void) {
    char addr[160], path[2048];

    g_nroutes = 0;
    parse_routes("/api=127.0.0.1:8080;/api/v2=127.0.0.1:9;/=3000");
    ck("/apiv2 -> /", route_for("/apiv2", addr, sizeof(addr), path, sizeof(path)) ? addr : "", "3000");
    ck("/apiary -> /", route_for("/apiary", addr, sizeof(addr), path, sizeof(path)) ? addr : "", "3000");
    ck("/api exact", route_for("/api", addr, sizeof(addr), path, sizeof(path)) ? addr : "", "127.0.0.1:8080");
    ck("/api/u", route_for("/api/u", addr, sizeof(addr), path, sizeof(path)) ? addr : "", "127.0.0.1:8080");
    ck("longest wins", route_for("/api/v2/x?q", addr, sizeof(addr), path, sizeof(path)) ? addr : "", "127.0.0.1:9");

    g_nroutes = 0;
    parse_routes("/api=127.0.0.1:8080,strip;/=3000,strip");
    route_for("/api/u?x=1&y=2", addr, sizeof(addr), path, sizeof(path));
    ck("strip keeps query", path, "/u?x=1&y=2");
    route_for("/api", addr, sizeof(addr), path, sizeof(path));
    ck("exact strip -> /", path, "/");
    route_for("/app?c=1", addr, sizeof(addr), path, sizeof(path));
    ck("strip root prefix = original", path, "/app?c=1");

    g_nroutes = 0;
    parse_routes("/api=127.0.0.1:8080");
    ck("no match -> 0", route_for("/other", addr, sizeof(addr), path, sizeof(path)) ? "hit" : "nil", "nil");

    if (failures == 0) { printf("c route tests: PASS\n"); return 0; }
    printf("c route tests: FAIL (%d)\n", failures);
    return 1;
}
