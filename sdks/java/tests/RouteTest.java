package dev.ggcode;

/** Multi-target routing semantics (mirrors sdks/node/route.js tests):
 *  run: javac -cp ../../build -d /tmp/et-jt tests/RouteTest.java && java -cp /tmp/et-jt dev.ggcode.RouteTest */
public class RouteTest {
    static int failures = 0;

    static void check(String name, Object got, Object want) {
        if (!String.valueOf(got).equals(String.valueOf(want))) {
            failures++;
            System.out.println("FAIL " + name + ": got " + got + " want " + want);
        }
    }

    public static void main(String[] args) {
        Etunnel.Route[] rs = Etunnel.normalizeRoutes(new Etunnel.Route[]{
            route("/", "fe:1"), route("/api", "be:1"), route("/api/v2", "be2:1")});
        check("/apiv2 -> /", Etunnel.routeFor(rs, "/apiv2").addr, "fe:1");
        check("/apiary -> /", Etunnel.routeFor(rs, "/apiary").addr, "fe:1");
        check("/api exact", Etunnel.routeFor(rs, "/api").addr, "be:1");
        check("/api/u", Etunnel.routeFor(rs, "/api/u").addr, "be:1");
        check("longest wins", Etunnel.routeFor(rs, "/api/v2/x?q").addr, "be2:1");

        Etunnel.Route[] ss = Etunnel.normalizeRoutes(new Etunnel.Route[]{
            route("/api", "be:1", true), route("/", "fe:1", true)});
        check("strip keeps query", Etunnel.routeFor(ss, "/api/u?x=1&y=2").path, "/u?x=1&y=2");
        check("exact strip -> /", Etunnel.routeFor(ss, "/api").path, "/");
        check("strip root prefix = original", Etunnel.routeFor(ss, "/app?c=1").path, "/app?c=1");

        Etunnel.Route[] one = Etunnel.normalizeRoutes(new Etunnel.Route[]{ route("/api", "be:1") });
        check("no match -> null", Etunnel.routeFor(one, "/other"), null);

        check("empty addr rejected", thrown(() -> Etunnel.normalizeRoutes(
            new Etunnel.Route[]{ route("/", "") })).contains("addr is required"), true);
        check("bare prefix rejected", thrown(() -> Etunnel.normalizeRoutes(
            new Etunnel.Route[]{ route("api", "a:1") })).contains("must start with"), true);
        check("duplicate rejected", thrown(() -> Etunnel.normalizeRoutes(
            new Etunnel.Route[]{ route("/api", "a:1"), route("/api/", "a:2") })).contains("duplicate"), true);

        if (failures == 0) System.out.println("java route tests: PASS");
        else { System.out.println("java route tests: FAIL (" + failures + ")"); System.exit(1); }
    }

    static String thrown(Runnable r) {
        try { r.run(); return "accepted"; }
        catch (IllegalArgumentException e) { return e.getMessage(); }
    }

    static Etunnel.Route route(String p, String a) { return route(p, a, false); }
    static Etunnel.Route route(String p, String a, boolean s) {
        Etunnel.Route r = new Etunnel.Route();
        r.prefix = p; r.addr = a; r.strip = s; return r;
    }
}
