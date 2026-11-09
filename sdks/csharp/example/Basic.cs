// Minimal etunnel example (C#, .NET file-based app or console project).
//   dotnet run --project example   (or: dotnet run example/Program.cs -- 3000)
using System;
using System.Net;
using System.Text;
using System.Threading.Tasks;

public static class ExampleBasic
{
    public static async Task Main(string[] args)
    {
        int port = args.Length > 0 ? int.Parse(args[0]) : 3000;
        var listener = new HttpListener();
        listener.Prefixes.Add($"http://127.0.0.1:{port}/");
        listener.Start();
        _ = Task.Run(async () =>
        {
            while (true)
            {
                var ctx = await listener.GetContextAsync();
                var body = Encoding.UTF8.GetBytes("hello from etunnel-example\n");
                ctx.Response.ContentType = "text/plain";
                ctx.Response.ContentLength64 = body.Length;
                await ctx.Response.OutputStream.WriteAsync(body);
                ctx.Response.Close();
            }
        });
        Console.WriteLine($"local http://127.0.0.1:{port}");
        var t = await Etunnel.Connect("wss://t.etun.uk/tunnel", port.ToString());
        Console.WriteLine($"public URL: {t.Url}");
        await Task.Delay(-1);
    }
}
