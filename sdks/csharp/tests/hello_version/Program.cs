// hello.version + client_outdated harness: one connect against the fixture
// (ETUNNEL_SERVER). Prints TUNNEL_URL on success; on a server error frame
// prints the message to stderr and exits 1 (CLI contract for client_outdated).
using System;

var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER")
    ?? throw new InvalidOperationException("ETUNNEL_SERVER required");
try
{
    var t = await Etunnel.Connect(server, "3000");
    Console.WriteLine($"TUNNEL_URL: {t.Url}");
    await t.Close();
    return 0;
}
catch (Exception e)
{
    Console.Error.WriteLine($"etunnel: {e.Message}");
    return 1;
}
