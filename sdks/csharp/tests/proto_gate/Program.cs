// #338 batch 2: proto gate harness - ETUNNEL_SERVER points at a
// ETUNNEL_FIXTURE_PROTO=2 fixture; a proto_version rejection is a PASS.
var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER")
    ?? throw new InvalidOperationException("ETUNNEL_SERVER required");
try
{
    var t = await Etunnel.Connect(server, "3000");
    Console.WriteLine($"FAIL: proto=2 welcome was accepted: {t.Url}");
    return 1;
}
catch (Exception e)
{
    if (e.Message.Contains("proto_version"))
    {
        Console.WriteLine("PASS: proto=2 rejected: " + e.Message);
        return 0;
    }
    Console.WriteLine("FAIL: unexpected: " + e.Message);
    return 1;
}
