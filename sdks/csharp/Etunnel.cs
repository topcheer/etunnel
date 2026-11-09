// etunnel SDK for C# / .NET 10 file-based app (zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run: dotnet run Etunnel.cs 3000
using System.Net.WebSockets;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;

var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER") ?? "wss://t.etun.uk/tunnel";
var local = args.Length > 0 ? args[0] : Environment.GetEnvironmentVariable("ETUNNEL_LOCAL") ?? "3000";
var token = Environment.GetEnvironmentVariable("ETUNNEL_TOKEN");
var subdomain = Environment.GetEnvironmentVariable("ETUNNEL_SUBDOMAIN");
// ETUNNEL_RECONNECT=0 restores the one-shot behaviour (exit on first error).
var reconnect = Environment.GetEnvironmentVariable("ETUNNEL_RECONNECT") != "0";

// Ctrl-C / SIGTERM stop the supervisor: the tunnel is closed and nothing is retried.
using var stop = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) => { e.Cancel = true; stop.Cancel(); };
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
        if (!reconnect) await Task.Delay(Timeout.Infinite);
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
        private readonly ClientWebSocket _ws;
        internal Tunnel(ClientWebSocket ws, string url, string resumeToken)
        {
            _ws = ws; Url = url; ResumeToken = resumeToken;
        }
        public async Task Close()
        {
            try { await SendText(_ws, "{\"type\":\"bye\",\"reason\":\"client closed\"}"); } catch { }
            await _ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", CancellationToken.None);
        }
    }

    public static async Task<Tunnel> Connect(string server, string local,
        string? token = null, string? subdomain = null, string? resume = null)
    {
        if (int.TryParse(local, out var port)) local = $"127.0.0.1:{port}";

        var ws = new ClientWebSocket();
        ws.Options.AddSubProtocol("etunnel-v1");
        await ws.ConnectAsync(new Uri(server), CancellationToken.None);

        var hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1");
        if (subdomain != null) hello.Append($",\"subdomain\":\"{subdomain}\"");
        if (token != null) hello.Append($",\"token\":\"{token}\"");
        if (!string.IsNullOrEmpty(resume))
            hello.Append($",\"resume\":\"{resume.Replace("\\", "\\\\").Replace("\"", "\\\"")}\"");
        hello.Append(",\"client\":\"etunnel-csharp/1.0\"}");
        await SendText(ws, hello.ToString());

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
                    throw new Exception($"etunnel: {doc.GetProperty("code").GetString()}");
                throw new Exception("etunnel: unexpected handshake message");
            }
        }

        var runner = new Runner(ws, local);
        _ = Task.Run(() => runner.Loop());
        return new Tunnel(ws, url, resumeToken);
    }

    sealed class Runner
    {
        readonly ClientWebSocket _ws;
        readonly string _local;
        readonly HttpClient _http = new() { Timeout = Timeout.InfiniteTimeSpan };
        readonly Dictionary<uint, (JsonElement req, List<byte[]> body)> _streams = new();

        internal Runner(ClientWebSocket ws, string local) { _ws = ws; _local = local; }

        internal async Task Loop()
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
                    await SendText(_ws, $"{{\"type\":\"pong\",\"ts\":{ts}}}");
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
                await SendText(_ws, json.ToString());
                for (var i = 0; i < payload.Length; i += MAXP)
                    await _ws.SendAsync(
                        new ArraySegment<byte>(Frame(0x01, sid, payload[i..Math.Min(i + MAXP, payload.Length)])),
                        WebSocketMessageType.Binary, true, CancellationToken.None);
                await _ws.SendAsync(new ArraySegment<byte>(Frame(0x02, sid, Array.Empty<byte>())),
                    WebSocketMessageType.Binary, true, CancellationToken.None);
            }
            catch (Exception e)
            {
                Console.Error.WriteLine($"[etunnel] serve error: {e}");
                await SendText(_ws, "{\"type\":\"res\",\"streamId\":" + sid +
                    ",\"status\":502,\"headers\":{\"Content-Type\":[\"text/plain\"]}}");
                var msg = Encoding.UTF8.GetBytes($"etunnel: local service error: {e.Message}\n");
                await _ws.SendAsync(new ArraySegment<byte>(Frame(0x01, sid, msg)),
                    WebSocketMessageType.Binary, true, CancellationToken.None);
                await _ws.SendAsync(new ArraySegment<byte>(Frame(0x02, sid, Array.Empty<byte>())),
                    WebSocketMessageType.Binary, true, CancellationToken.None);
            }
        }
    }

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

    static Task SendText(ClientWebSocket ws, string text) =>
        ws.SendAsync(new ArraySegment<byte>(Encoding.UTF8.GetBytes(text)),
            WebSocketMessageType.Text, true, CancellationToken.None);

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
