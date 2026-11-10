// etunnel SDK for Dart (dart:io only, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run: dart run etunnel.dart 3000
import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

const int MAXP = 32768;

Uint8List frame(int kind, int id, List<int> payload) {
  final b = BytesBuilder();
  b.addByte(kind);
  b.add([
    (id >> 24) & 0xFF, (id >> 16) & 0xFF, (id >> 8) & 0xFF, id & 0xFF,
    (payload.length >> 24) & 0xFF, (payload.length >> 16) & 0xFF,
    (payload.length >> 8) & 0xFF, payload.length & 0xFF,
  ]);
  b.add(payload);
  return b.toBytes();
}

const HOP = {'connection', 'proxy-connection', 'keep-alive', 'proxy-authenticate',
  'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade',
  'content-length', 'host'};

class Etunnel {
  final WebSocket ws;
  final HttpClient http = HttpClient();
  final String host;
  final int port;
  final _streams = <int, _StreamBuf>{};
  final _url = Completer<String>();

  Etunnel._(this.ws, this.host, this.port) {
    // onError: a decode/desync failure used to surface as an unhandled async
    // error and kill the isolate; log it and drop the connection instead
    // (#53 LOW).
    ws.listen(_onMessage, onDone: () {}, onError: (Object e) {
      stderr.writeln('etunnel: connection error: $e');
      try {
        ws.close();
      } catch (_) {/* already closed */}
    });
  }

  static Future<Etunnel> connect(String server, String local,
      {String? token, String? subdomain, String? resume}) async {
    var (host, port) = _parseLocal(local);
    final ws = await WebSocket.connect(server, protocols: ['etunnel-v1']);
    final et = Etunnel._(ws, host, port);
    ws.add(jsonEncode({
      'type': 'hello', 'proto': 1,
      if (subdomain != null) 'subdomain': subdomain,
      if (token != null) 'token': token,
      if (resume != null && resume.isNotEmpty) 'resume': resume,
      'client': 'etunnel-dart/1.0',
    }));
    await et._url.future.timeout(const Duration(seconds: 15));
    return et;
  }

  static (String, int) _parseLocal(String local) {
    if (RegExp(r'^\d+$').hasMatch(local)) return ('127.0.0.1', int.parse(local));
    final i = local.lastIndexOf(':');
    return (local.substring(0, i), int.parse(local.substring(i + 1)));
  }

  String? url;
  /// Server-signed token from the welcome message; feeding it back on the next
  /// connect keeps the same subdomain instead of a fresh random one.
  String? resumeToken;

  void _onMessage(Object? msg) {
    if (msg is String) {
      final m = jsonDecode(msg) as Map<String, dynamic>;
      switch (m['type']) {
        case 'welcome':
          url = m['url'] as String;
          resumeToken = (m['resumeToken'] as String?) ?? resumeToken;
          _url.complete(url!);
        case 'error':
          if (!_url.isCompleted) {
            _url.completeError('etunnel: ${m['code']}: ${m['message'] ?? ''}');
          }
        case 'req':
          _streams[m['streamId'] as int] = _StreamBuf(m);
        case 'ping':
          _send(jsonEncode({'type': 'pong', 'ts': m['ts'] ?? 0}));
        case 'bye':
          ws.close();
      }
      return;
    }
    final b = msg is List<int> ? Uint8List.fromList(msg) : msg as Uint8List;
    if (b.length < 9) return;
    final kind = b[0];
    final sid = (b[1] << 24) | (b[2] << 16) | (b[3] << 8) | b[4];
    final n = (b[5] << 24) | (b[6] << 16) | (b[7] << 8) | b[8];
    // A truncated frame (n beyond the actual payload) used to raise an
    // unhandled RangeError here (#53 MED); clamp like the C#/Elixir SDKs.
    final payloadEnd = 9 + n > b.length ? b.length : 9 + n;
    final payload = b.sublist(9, payloadEnd);
    final st = _streams[sid];
    if (st == null) return;
    switch (kind) {
      case 0x01: st.body.add(payload);
      case 0x02:
        _streams.remove(sid);
        _serve(st, sid);
      case 0x03: _streams.remove(sid);
    }
  }

  /// ws.add on a closed sink throws StateError; every outbound send goes
  /// through here so a drop mid-serve cannot kill the isolate (#53 LOW).
  void _send(Object data) {
    try {
      ws.add(data);
    } on StateError {
      // connection already gone; nothing to deliver
    }
  }

  Future<void> _serve(_StreamBuf st, int sid) async {
    final req = st.req;
    final method = req['method'] as String? ?? 'GET';
    final path = req['path'] as String? ?? '/';
    try {
      final rq = await http.openUrl(method, Uri.http('$host:$port', path));
      (req['headers'] as Map<String, dynamic>? ?? {}).forEach((k, vs) {
        if (HOP.contains(k.toLowerCase())) return;
        for (final v in vs) rq.headers.add(k, v.toString());
      });
      if (method != 'GET' && method != 'HEAD' && st.body.isNotEmpty) {
        rq.add(st.body.takeBytes());
      }
      await rq.flush();
      final rs = await rq.close();
      final hdrs = <String, List<String>>{};
      rs.headers.forEach((k, vs) {
        if (!HOP.contains(k.toLowerCase())) hdrs[k] = vs;
      });
      _send(jsonEncode({'type': 'res', 'streamId': sid, 'status': rs.statusCode, 'headers': hdrs}));
      final body = await rs.fold<BytesBuilder>(BytesBuilder(), (b, d) { b.add(d); return b; }).then((b) => b.toBytes());
      for (var i = 0; i < body.length; i += MAXP) {
        _send(frame(0x01, sid, body.sublist(i, i + MAXP > body.length ? body.length : i + MAXP)));
      }
      _send(frame(0x02, sid, []));
    } catch (e) {
      _send(jsonEncode({'type': 'res', 'streamId': sid, 'status': 502,
        'headers': {'Content-Type': ['text/html; charset=utf-8']}}));
      _send(frame(0x01, sid, utf8.encode(_localErrorPage(e))));
      _send(frame(0x02, sid, []));
    }
  }

  // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
  String _localErrorPage(Object error) {
    final esc = '$error'
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;');
    return '''
<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
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
<span class="badge">502 local app down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class="panel">
<div class="k">error</div><div class="v err">$esc</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>''';
  }

  void close() {
    _send(jsonEncode({'type': 'bye', 'reason': 'client closed'}));
    ws.close();
    http.close(); // the client and its sockets must not outlive the tunnel (#53 MED)
  }
}

class _StreamBuf {
  final Map<String, dynamic> req;
  final BytesBuilder body = BytesBuilder();
  _StreamBuf(this.req);
}

int _backoffMs(int attempt) {
  // 500ms, 1s, 2s, ... capped at 30s, then ±20% jitter.
  final base = (500 * (1 << (attempt > 6 ? 6 : attempt))).clamp(0, 30000);
  final pct = 800 + DateTime.now().microsecondsSinceEpoch % 401; // 800..1200‰
  return base * pct ~/ 1000;
}

Future<void> main(List<String> args) async {
  const String kDefaultServer = String.fromEnvironment(
        'ETUNNEL_DEFAULT_SERVER',
        defaultValue: 'wss://t.etun.uk/tunnel');
    final server = Platform.environment['ETUNNEL_SERVER'] ?? kDefaultServer;
  final local = args.isNotEmpty ? args[0] : Platform.environment['ETUNNEL_LOCAL'] ?? '3000';
  final token = Platform.environment['ETUNNEL_TOKEN'];
  final subdomain = Platform.environment['ETUNNEL_SUBDOMAIN'];
  // ETUNNEL_RECONNECT=0 restores the one-shot behaviour.
  final reconnect = Platform.environment['ETUNNEL_RECONNECT'] != '0';

  // Ctrl-C / SIGTERM stop the supervisor: the tunnel is closed and no
  // reconnection is attempted.
  var stopped = false;
  final done = Completer<void>(); // unblocks the one-shot wait on a signal
  Etunnel? current;
  final signals = <StreamSubscription<ProcessSignal>>[];
  for (final sig in [ProcessSignal.sigint, ProcessSignal.sigterm]) {
    try {
      signals.add(sig.watch().listen((_) {
        stopped = true;
        if (!done.isCompleted) done.complete(); // else the one-shot await hangs forever
        try {
          current?.close(); // unblocks ws.done so the loop can exit
        } catch (_) {
          // socket already gone; nothing left to close
        }
      }));
    } catch (_) {
      // signals are not supported on every platform
    }
  }

  var attempt = 0;
  // Carried across drops so a reconnect reclaims the same subdomain.
  String? resume;
  while (!stopped) {
    try {
      final t = await Etunnel.connect(server, local,
          token: token, subdomain: subdomain, resume: resume);
      current = t;
      resume = t.resumeToken ?? resume;
      print('TUNNEL_URL: ${t.url}');
      attempt = 0;
      if (!reconnect) {
        // Run until a signal: the completer is finished by the signal handler
        // above, so the wait is cancellable (same class of fix as the C# and
        // Swift one-shot hangs).
        await done.future;
      }
      await t.ws.done; // completes when the connection ends
      current = null;
      if (stopped) break;
      stderr.writeln('etunnel: connection lost, reconnecting');
    } catch (e) {
      current = null;
      stderr.writeln('etunnel: $e');
      if (!reconnect) exit(1);
      if (stopped) break;
    }
    final wait = _backoffMs(attempt);
    attempt++;
    stderr.writeln('etunnel: reconnect in ${wait}ms');
    final deadline = DateTime.now().add(Duration(milliseconds: wait));
    while (!stopped && DateTime.now().isBefore(deadline)) {
      await Future.delayed(const Duration(milliseconds: 100));
    }
  }
  for (final s in signals) {
    await s.cancel();
  }
  exit(0);
}
