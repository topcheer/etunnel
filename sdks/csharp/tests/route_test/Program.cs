// Route table tests (same semantics table as sdks/node/route.js).
// Run: dotnet run --project tests/route_test
using System;

internal static class RouteTestMain
{
    private static int failures = 0;

    private static void Ck(string name, object? got, object? want)
    {
        if (!Equals(got?.ToString(), want?.ToString()))
        {
            failures++;
            Console.Error.WriteLine($"FAIL {name}: got {got} want {want}");
        }
    }

    private static string Msg(Action a)
    {
        try { a(); return "accepted"; }
        catch (ArgumentException e) { return e.Message; }
    }

    private static int Main()
    {
        var rs = Etunnel.NormalizeRoutes(new[] {
            new Etunnel.Route { Prefix = "/", Addr = "fe:1" },
            new Etunnel.Route { Prefix = "/api", Addr = "be:1" },
            new Etunnel.Route { Prefix = "/api/v2", Addr = "be2:1" },
        });
        Ck("/apiv2 -> /", Etunnel.RouteFor(rs, "/apiv2")!.Value.Addr, "fe:1");
        Ck("/apiary -> /", Etunnel.RouteFor(rs, "/apiary")!.Value.Addr, "fe:1");
        Ck("/api exact", Etunnel.RouteFor(rs, "/api")!.Value.Addr, "be:1");
        Ck("/api/u", Etunnel.RouteFor(rs, "/api/u")!.Value.Addr, "be:1");
        Ck("longest wins", Etunnel.RouteFor(rs, "/api/v2/x?q")!.Value.Addr, "be2:1");

        var ss = Etunnel.NormalizeRoutes(new[] {
            new Etunnel.Route { Prefix = "/api", Addr = "be:1", Strip = true },
            new Etunnel.Route { Prefix = "/", Addr = "fe:1", Strip = true },
        });
        Ck("strip keeps query", Etunnel.RouteFor(ss, "/api/u?x=1&y=2")!.Value.Path, "/u?x=1&y=2");
        Ck("exact strip -> /", Etunnel.RouteFor(ss, "/api")!.Value.Path, "/");
        Ck("strip root prefix = original", Etunnel.RouteFor(ss, "/app?c=1")!.Value.Path, "/app?c=1");

        var one = Etunnel.NormalizeRoutes(new[] { new Etunnel.Route { Prefix = "/api", Addr = "be:1" } });
        Ck("no match -> null", Etunnel.RouteFor(one, "/other"), null);

        Ck("addr required", Msg(() => Etunnel.NormalizeRoutes(new[] { new Etunnel.Route { Prefix = "/", Addr = "" } })).Contains("addr is required") ? "rejected" : "accepted", "rejected");
        Ck("must start with /", Msg(() => Etunnel.NormalizeRoutes(new[] { new Etunnel.Route { Prefix = "api", Addr = "a:1" } })).Contains("must start with") ? "rejected" : "accepted", "rejected");
        Ck("duplicate", Msg(() => Etunnel.NormalizeRoutes(new[] {
            new Etunnel.Route { Prefix = "/api", Addr = "a:1" },
            new Etunnel.Route { Prefix = "/api/", Addr = "a:2" },
        })).Contains("duplicate") ? "rejected" : "accepted", "rejected");

        Console.WriteLine(failures == 0 ? "csharp route tests: PASS" : $"csharp route tests: FAIL ({failures})");
        return failures == 0 ? 0 : 1;
    }
}
