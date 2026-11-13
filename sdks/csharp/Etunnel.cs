// etunnel SDK for C# / .NET 10 file-based app (zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run: dotnet run Etunnel.cs 3000
using System.Net.WebSockets;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;

// CLI entrypoint. Wrapped in an explicit Main (instead of top-level
// statements) so embedders can compile this file into their own exe without
// the CLI supervisor running on load; Etunnel.csproj pins StartupObject=Cli.
internal static class Cli {
    public static async Task Main(string[] args) {
var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER") ?? "wss://t.etun.uk/tunnel";
var local = args.Length > 0 ? args[0] : Environment.GetEnvironmentVariable("ETUNNEL_LOCAL") ?? "3000";
var token = Environment.GetEnvironmentVariable("ETUNNEL_TOKEN");
var subdomain = Environment.GetEnvironmentVariable("ETUNNEL_SUBDOMAIN");
// ETUNNEL_RECONNECT=0 restores the one-shot behaviour (exit on first error).
var reconnect = Environment.GetEnvironmentVariable("ETUNNEL_RECONNECT") != "0";

// Ctrl-C / SIGTERM stop the supervisor: the tunnel is closed and nothing is
// retried. Both are registered as POSIX signal handlers: Console.CancelKeyPress
// is delivered by the terminal driver and never fires when stdin is not a TTY
// (background service, nohup, CI), and its runtime handler also consumes the
// signal before a PosixSignalRegistration can see it (#54 MED).
using var stop = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) => { e.Cancel = true; stop.Cancel(); };
// Console.CancelKeyPress is delivered by the terminal driver and never fires
// when stdin is not a TTY (background service, nohup, CI) - a supervisor run
// that way swallowed SIGINT and hung. The POSIX registration covers exactly
// those cases; both paths just cancel the same token (#54 MED).
using var sigint = PosixSignalRegistration.Create(PosixSignal.SIGINT, ctx =>
{
    ctx.Cancel = true;
    stop.Cancel();
});
using var sigterm = PosixSignalRegistration.Create(PosixSignal.SIGTERM, ctx =>
{
    ctx.Cancel = true;
    stop.Cancel();
});

var attempt = 0;
// Carried across drops so a reconnect reclaims the same subdomain.
string? resume = null;
while (!stop.IsCancellationRequested)
{
    try
    {
        var t = await Etunnel.Connect(server, local, token: token, subdomain: subdomain, resume: resume);
        if (t.ResumeToken.Length > 0) resume = t.ResumeToken;
        Console.WriteLine($"TUNNEL_URL: {t.Url}");
        attempt = 0;
        if (!reconnect)
        {
            // One-shot mode must still observe Ctrl-C: a token-less infinite
            // delay hung forever because SIGINT only set e.Cancel (#54 MED).
            try { await Task.Delay(Timeout.Infinite, stop.Token); }
            catch (TaskCanceledException) { }
        }
        while (!stop.IsCancellationRequested && t.State == WebSocketState.Open)
            await Task.Delay(200);
        try { await t.Close(); } catch { }
        if (stop.IsCancellationRequested) break;
        Console.Error.WriteLine("[etunnel] connection lost, reconnecting");
    }
    catch (Exception e)
    {
        if (stop.IsCancellationRequested) break;
        Console.Error.WriteLine($"etunnel: {e.Message}");
        if (!reconnect) Environment.Exit(1);
    }

    var wait = BackoffMs(attempt);
    attempt++;
    Console.Error.WriteLine($"[etunnel] reconnect in {wait}ms");
    try { await Task.Delay((int)wait, stop.Token); }
    catch (TaskCanceledException) { break; }
}

// 500ms, 1s, 2s, ... capped at 30s, then ±20% jitter.
static long BackoffMs(int attempt)
{
    var baseMs = Math.Min(30_000L, 500L << Math.Min(attempt, 6));
    return (long)Math.Round(baseMs * (0.8 + Random.Shared.NextDouble() * 0.4));
}
    } // CLI supervisor loop end
} // end internal static class Cli

public sealed class Etunnel
{
    const int MAXP = 32768;

    public sealed class Tunnel
    {
        public string Url { get; }
        /// <summary>Live socket state; the CLI supervisor polls it to detect a drop.</summary>
        public WebSocketState State => _ws.State;
        /// <summary>Server-signed token; send it back on reconnect to reclaim the same subdomain.</summary>
        public string ResumeToken { get; }
        readonly ClientWebSocket _ws;
        readonly Sender _sender;
        internal Tunnel(ClientWebSocket ws, Sender sender, string url, string resumeToken) => (_ws, _sender, Url, ResumeToken) = (ws, sender, url, resumeToken);
        public async Task Close()
        {
            try { await _sender.Atomic(() => _sender.SendText("{\"type\":\"bye\",\"reason\":\"client closed\"}")); }
            catch { }
            try
            {
                // Bounded close handshake: a wedged peer that never answers the
                // close frame would otherwise hang Close (and the supervisor)
                // forever - CancellationToken.None meant no way out.
                using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(2));
                await _ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", cts.Token);
            }
            catch (Exception) { /* peer gone or wedged: dispose regardless */ }
            _ws.Dispose();
        }
    }

    /// <summary>
    /// ClientWebSocket allows exactly one outstanding SendAsync; concurrent
    /// streams plus pong replies used to race into InvalidOperationException
    /// or interleaved frames (#54 HIGH). Every outbound frame - and each
    /// res+DATA+FIN sequence, which must never interleave with another
    /// stream's - goes through this gate.
    /// </summary>
    internal sealed class Sender
    {
        readonly ClientWebSocket _ws;
        readonly SemaphoreSlim _gate = new(1, 1);
        internal Sender(ClientWebSocket ws) => _ws = ws;

        internal async Task Atomic(Func<Task> sequence)
        {
            await _gate.WaitAsync();
            try { await sequence(); }
            finally { _gate.Release(); }
        }

        internal Task SendText(string text) =>
            _ws.SendAsync(new ArraySegment<byte>(Encoding.UTF8.GetBytes(text)),
                WebSocketMessageType.Text, true, CancellationToken.None);

        internal Task SendBinary(byte[] frame) =>
            _ws.SendAsync(new ArraySegment<byte>(frame),
                WebSocketMessageType.Binary, true, CancellationToken.None);
    }

    public static async Task<Tunnel> Connect(string server, string local,
        string? token = null, string? subdomain = null, string? resume = null)
    {
        if (int.TryParse(local, out var port)) local = $"127.0.0.1:{port}";

        var ws = new ClientWebSocket();
        ws.Options.AddSubProtocol("etunnel-v1");
        var sender = new Sender(ws);
        try
        {
        // The handshake must not hang forever on a black-holed address (#54 LOW).
        using (var dialCts = new CancellationTokenSource(TimeSpan.FromSeconds(15)))
            await ws.ConnectAsync(new Uri(server), dialCts.Token);

        var hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1");
        if (subdomain != null) hello.Append($",\"subdomain\":\"{Escape(subdomain)}\"");
        if (token != null) hello.Append($",\"token\":\"{Escape(token)}\"");
        if (!string.IsNullOrEmpty(resume))
            hello.Append($",\"resume\":\"{resume.Replace("\\", "\\\\").Replace("\"", "\\\"")}\"");
        hello.Append(",\"client\":\"etunnel-csharp/1.0\"}");
        await sender.SendText(hello.ToString());

        string url;
        var resumeToken = "";
        using (var cts = new CancellationTokenSource(TimeSpan.FromSeconds(15)))
        {
            while (true)
            {
                var (kind, bytes) = await ReadMessage(ws, cts.Token);
                if (kind != WebSocketMessageType.Text)
                    throw new Exception("etunnel: unexpected handshake message");
                var doc = JsonDocument.Parse(Encoding.UTF8.GetString(bytes)).RootElement;
                var type = doc.GetProperty("type").GetString();
                if (type == "welcome")
                {
                    url = doc.GetProperty("url").GetString()!;
                    if (doc.TryGetProperty("resumeToken", out var rt))
                        resumeToken = rt.GetString() ?? "";
                    break;
                }
                if (type == "error")
                {
                    // keep the message field: a bare code in a log is useless (#54 LOW)
                    var code = doc.GetProperty("code").GetString();
                    doc.TryGetProperty("message", out var msg);
                    throw new Exception($"etunnel: {code}: {msg.GetString() ?? ""}");
                }
                throw new Exception("etunnel: unexpected handshake message");
            }
        }

        var runner = new Runner(ws, sender, local);
        _ = Task.Run(() => runner.Loop());
        return new Tunnel(ws, sender, url, resumeToken);
        }
        catch
        {
            ws.Dispose(); // a failed handshake must not leak the socket (#54 LOW)
            throw;
        }
    }

    sealed class Runner
    {
        readonly ClientWebSocket _ws;
        readonly Sender _sender;
        readonly string _local;
        readonly HttpClient _http = new() { Timeout = Timeout.InfiniteTimeSpan };
        readonly Dictionary<uint, (JsonElement req, List<byte[]> body)> _streams = new();

        internal Runner(ClientWebSocket ws, Sender sender, string local)
        {
            _ws = ws; _sender = sender; _local = local;
        }

        internal async Task Loop()
        {
            // One HttpClient per connection, released with it: the supervisor's
            // reconnect loop used to accumulate clients (and their pools)
            // forever (#54 MED).
            try
            {
                while (_ws.State == WebSocketState.Open)
            {
                WebSocketMessageType kind;
                byte[] bytes;
                try
                {
                    (kind, bytes) = await ReadMessage(_ws, CancellationToken.None);
                }
                catch (Exception e) { Console.Error.WriteLine($"[etunnel] loop error: {e}"); return; }
                if (kind == WebSocketMessageType.Text)
                {
                    try { await HandleText(Encoding.UTF8.GetString(bytes)); }
                    catch (Exception e) { Console.Error.WriteLine($"[etunnel] text error: {e}"); }
                }
                else
                {
                    try { HandleBinary(bytes); }
                    catch (Exception e) { Console.Error.WriteLine($"[etunnel] binary error: {e}"); }
                }
                }
            }
            finally
            {
                _http.Dispose();
            }
        }

        async Task HandleText(string text)
        {
            using var doc = JsonDocument.Parse(text);
            var m = doc.RootElement;
            switch (m.GetProperty("type").GetString())
            {
                case "req":
                    _streams[(uint)m.GetProperty("streamId").GetInt64()] = (m.Clone(), new List<byte[]>());
                    break;
                case "ping":
                    long ts = m.TryGetProperty("ts", out var tse) ? tse.GetInt64() : 0;
                    await _sender.Atomic(() => _sender.SendText($"{{\"type\":\"pong\",\"ts\":{ts}}}"));
                    break;
                case "bye":
                    await _ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "server closed", CancellationToken.None);
                    break;
            }
        }

        void HandleBinary(byte[] b)
        {
            if (b.Length < 9) return;
            int kind = b[0];
            uint sid = (uint)((b[1] << 24) | (b[2] << 16) | (b[3] << 8) | b[4]);
            int n = (b[5] << 24) | (b[6] << 16) | (b[7] << 8) | b[8];
            if (!_streams.TryGetValue(sid, out var st)) return;
            switch (kind)
            {
                case 0x01:
                    st.body.Add(b[9..Math.Min(9 + n, b.Length)]);
                    _streams[sid] = st;
                    break;
                case 0x02:
                    _streams.Remove(sid);
                    _ = Serve(st.req, st.body.SelectMany(x => x).ToArray(), sid);
                    break;
                case 0x03:
                    _streams.Remove(sid);
                    break;
            }
        }

        async Task Serve(JsonElement req, byte[] body, uint sid)
        {
            var method = req.GetProperty("method").GetString()!;
            var path = req.GetProperty("path").GetString()!;
            try
            {
                using var hr = new HttpRequestMessage(new HttpMethod(method), $"http://{_local}{path}");
                if (req.TryGetProperty("headers", out var hdrs))
                    foreach (var kv in hdrs.EnumerateObject())
                    {
                        var lk = kv.Name.ToLowerInvariant();
                        if (lk is "connection" or "transfer-encoding" or "keep-alive" or "upgrade"
                            or "content-length" or "host" or "proxy-connection") continue;
                        foreach (var v in kv.Value.EnumerateArray())
                            hr.Headers.TryAddWithoutValidation(kv.Name, v.GetString());
                    }
                if (method != "GET" && method != "HEAD" && body.Length > 0)
                    hr.Content = new ByteArrayContent(body);

                using var resp = await _http.SendAsync(hr);
                var payload = await resp.Content.ReadAsByteArrayAsync();

                var hdrObj = new Dictionary<string, List<string>>();
                foreach (var kv in resp.Headers)
                {
                    var lk = kv.Key.ToLowerInvariant();
                    if (lk is "connection" or "transfer-encoding" or "keep-alive") continue;
                    hdrObj[kv.Key] = kv.Value.ToList();
                }
                foreach (var kv in resp.Content.Headers)
                {
                    if (kv.Key.ToLowerInvariant() is "content-length") continue;
                    if (!hdrObj.TryAdd(kv.Key, kv.Value.ToList()))
                        hdrObj[kv.Key].AddRange(kv.Value);
                }

                // File-based apps disable reflection-based JSON: build manually.
                var json = new StringBuilder("{\"type\":\"res\",\"streamId\":").Append(sid)
                    .Append(",\"status\":").Append((int)resp.StatusCode).Append(",\"headers\":{");
                var first = true;
                foreach (var kv in hdrObj)
                {
                    if (!first) json.Append(',');
                    first = false;
                    json.Append('"').Append(Escape(kv.Key)).Append("\":[");
                    var f2 = true;
                    foreach (var v in kv.Value)
                    {
                        if (!f2) json.Append(',');
                        f2 = false;
                        json.Append('"').Append(Escape(v)).Append('"');
                    }
                    json.Append(']');
                }
                json.Append("}}");
                // The whole res+DATA+FIN sequence holds the send gate: another
                // stream's frames must never interleave mid-sequence (#54 HIGH).
                await _sender.Atomic(async () =>
                {
                    await _sender.SendText(json.ToString());
                    for (var i = 0; i < payload.Length; i += MAXP)
                        await _sender.SendBinary(Frame(0x01, sid, payload[i..Math.Min(i + MAXP, payload.Length)]));
                    await _sender.SendBinary(Frame(0x02, sid, Array.Empty<byte>()));
                });
            }
            catch (Exception e)
            {
                Console.Error.WriteLine($"[etunnel] serve error: {e}");
                // The tunnel may already be closed and disposed (supervisor
                // reconnect while this serve was in flight): the fallback
                // sends must not become an unobserved task exception.
                try
                {
                    await _sender.Atomic(async () =>
                    {
                        await _sender.SendText("{\"type\":\"res\",\"streamId\":" + sid +
                            ",\"status\":502,\"headers\":{\"Content-Type\":[\"text/html; charset=utf-8\"]}}");
                        var msg = Encoding.UTF8.GetBytes(LocalErrorPage(e.Message));
                        await _sender.SendBinary(Frame(0x01, sid, msg));
                        await _sender.SendBinary(Frame(0x02, sid, Array.Empty<byte>()));
                    });
                }
                catch { /* ws gone: nothing left to deliver */ }
            }
        }
    }

    // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
    static string LocalErrorPage(string msg)
    {
        var esc = msg.Replace("&", "&amp;").Replace("<", "&lt;").Replace(">", "&gt;");
        return ErrPageA + esc + ErrPageB;
    }

    const string ErrPageA = @"<!doctype html><html><head><meta charset=""utf-8"">
<meta name=""viewport"" content=""width=device-width, initial-scale=1"">
<title>502 - local app down</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#f85149}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#58a6ff;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class=""badge"">502 local app down</span>
<h1>tunnel up, <span class=""g"">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class=""panel"">
<div class=""k"">error</div><div class=""v err"">";

    const string ErrPageB = @"</div>
</div>
<div class=""tip""><span class=""pulse""></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href=""https://github.com/topcheer/etunnel"">etunnel</a></footer>
</main></body></html>";

    static string Escape(string s) =>
        s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\n", "\\n").Replace("\r", "\\r").Replace("\t", "\\t");

    static byte[] Frame(byte kind, uint sid, byte[] payload)
    {
        var f = new byte[9 + payload.Length];
        f[0] = kind;
        f[1] = (byte)(sid >> 24); f[2] = (byte)(sid >> 16); f[3] = (byte)(sid >> 8); f[4] = (byte)sid;
        var n = (uint)payload.Length;
        f[5] = (byte)(n >> 24); f[6] = (byte)(n >> 16); f[7] = (byte)(n >> 8); f[8] = (byte)n;
        payload.CopyTo(f, 9);
        return f;
    }

    static async Task<(WebSocketMessageType, byte[])> ReadMessage(ClientWebSocket ws, CancellationToken ct)
    {
        using var ms = new MemoryStream();
        var buf = new byte[64 * 1024];
        while (true)
        {
            var r = await ws.ReceiveAsync(new ArraySegment<byte>(buf), ct);
            if (r.MessageType == WebSocketMessageType.Close) throw new IOException("closed");
            ms.Write(buf, 0, r.Count);
            if (r.EndOfMessage) return (r.MessageType, ms.ToArray());
        }
    }
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
//   var creds = await EtunnelLogin.LoginAsync("https://t.etun.uk");
// Binds a random 127.0.0.1 port, waits for the one-time code (10-minute
// budget), exchanges it. Persist it; pass ApiToken as the token option.
public sealed record LoginCredentials(string ApiToken, IReadOnlyList<string> Subdomains, string Plan);

public static class EtunnelLogin
{
    public static async Task<LoginCredentials> LoginAsync(string server,
        Func<string, Task>? openBrowser = null, CancellationToken ct = default)
    {
        var baseUri = server.TrimEnd('/');
        if (baseUri.Length == 0) throw new ArgumentException("etunnel: login: server required");

        // TcpListener claims an ephemeral port; HttpListener needs the fixed prefix.
        int port;
        using (var probe = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0))
        {
            probe.Start();
            port = ((System.Net.IPEndPoint)probe.LocalEndpoint).Port;
            probe.Stop();
        }
        var listener = new System.Net.HttpListener();
        listener.Prefixes.Add($"http://127.0.0.1:{port}/callback/");
        listener.Start();
        try
        {
            using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(30) };
            var startJson = await PostJsonAsync(http, $"{baseUri}/auth/cli/start",
                $"{{\"redirect_uri\":\"http://127.0.0.1:{port}/callback\"}}", ct);
            using var start = System.Text.Json.JsonDocument.Parse(startJson);
            var authUrl = start.RootElement.TryGetProperty("auth_url", out var au) ? au.GetString() : null;
            var deviceCode = start.RootElement.TryGetProperty("device_code", out var dc) ? dc.GetString() : null;
            if (string.IsNullOrEmpty(authUrl) || string.IsNullOrEmpty(deviceCode))
                throw new InvalidOperationException("etunnel: login: empty start payload");

            var opened = false;
            if (openBrowser != null) { await openBrowser(authUrl!); opened = true; }
            else
            {
                try
                {
                    using var _ = System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(authUrl!) { UseShellExecute = true });
                    opened = true;
                }
                catch { /* headless: URL printed below */ }
            }
            if (!opened) Console.Error.WriteLine($"etunnel: authorize in your browser: {authUrl}");

            var code = await GetCodeAsync(listener, TimeSpan.FromMinutes(10), ct);
            var credsJson = await PostJsonAsync(http, $"{baseUri}/auth/cli/exchange",
                $"{{\"device_code\":\"{deviceCode}\",\"code\":\"{code}\"}}", ct);
            using var doc = System.Text.Json.JsonDocument.Parse(credsJson);
            var tok = doc.RootElement.TryGetProperty("api_token", out var t) ? t.GetString() : null;
            if (string.IsNullOrEmpty(tok)) throw new InvalidOperationException("etunnel: login: no api_token in exchange response");
            var subs = doc.RootElement.TryGetProperty("subdomains", out var sarr) && sarr.ValueKind == System.Text.Json.JsonValueKind.Array
                ? sarr.EnumerateArray().Select(x => x.ToString()).ToList()
                : new List<string>();
            var plan = doc.RootElement.TryGetProperty("plan", out var pl) ? pl.GetString() ?? "" : "";
            return new LoginCredentials(tok!, subs, plan);
        }
        finally { listener.Stop(); listener.Close(); }
    }

    private static async Task<string> GetCodeAsync(System.Net.HttpListener listener, TimeSpan timeout, CancellationToken ct)
    {
        using var cts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        cts.CancelAfter(timeout);
        while (!cts.IsCancellationRequested)
        {
            var ctx = await listener.GetContextAsync().WaitAsync(cts.Token);
            var code = ctx.Request.QueryString["code"];
            var body = System.Text.Encoding.UTF8.GetBytes(
                "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>");
            ctx.Response.StatusCode = string.IsNullOrEmpty(code) ? 400 : 200;
            ctx.Response.ContentType = "text/html; charset=utf-8";
            ctx.Response.Close(body, false);
            if (!string.IsNullOrEmpty(code)) return code;
        }
        throw new TimeoutException("etunnel: login timed out");
    }

    private static async Task<string> PostJsonAsync(HttpClient http, string url, string json, CancellationToken ct)
    {
        using var resp = await http.PostAsync(url, new StringContent(json, System.Text.Encoding.UTF8, "application/json"), ct);
        var body = await resp.Content.ReadAsStringAsync(ct);
        if (!resp.IsSuccessStatusCode)
            throw new InvalidOperationException($"etunnel: login: HTTP {(int)resp.StatusCode} from {url}: {body.Substring(0, Math.Min(200, body.Length))}");
        return body;
    }
}
