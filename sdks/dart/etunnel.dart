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
    ws.listen(_onMessage, onDone: () {});
  }

  static Future<Etunnel> connect(String server, String local,
      {String? token, String? subdomain}) async {
    var (host, port) = _parseLocal(local);
    final ws = await WebSocket.connect(server, protocols: ['etunnel-v1']);
    final et = Etunnel._(ws, host, port);
    ws.add(jsonEncode({
      'type': 'hello', 'proto': 1,
      if (subdomain != null) 'subdomain': subdomain,
      if (token != null) 'token': token,
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

  void _onMessage(Object? msg) {
    if (msg is String) {
      final m = jsonDecode(msg) as Map<String, dynamic>;
      switch (m['type']) {
        case 'welcome':
          url = m['url'] as String;
          _url.complete(url!);
        case 'error':
          if (!_url.isCompleted) {
            _url.completeError('etunnel: ${m['code']}: ${m['message'] ?? ''}');
          }
        case 'req':
          _streams[m['streamId'] as int] = _StreamBuf(m);
        case 'ping':
          ws.add(jsonEncode({'type': 'pong', 'ts': m['ts'] ?? 0}));
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
    final payload = b.sublist(9, 9 + n);
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

  Future<void> _serve(_StreamBuf st, int sid) async {
    final req = st.req;
    final method = req['method'] as String? ?? 'GET';
    final path = req['path'] as String? ?? '/';
    try {
      final rq = await http.openUrl(method, Uri.http('$host:$port', path));
      (req['headers'] as Map<String, dynamic>? ?? {}).forEach((k, vs) {
        if (HOP.contains((k as String).toLowerCase())) return;
        for (final v in (vs as List)) rq.headers.add(k, v.toString());
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
      ws.add(jsonEncode({'type': 'res', 'streamId': sid, 'status': rs.statusCode, 'headers': hdrs}));
      final body = await rs.fold<BytesBuilder>(BytesBuilder(), (b, d) { b.add(d); return b; }).then((b) => b.toBytes());
      for (var i = 0; i < body.length; i += MAXP) {
        ws.add(frame(0x01, sid, body.sublist(i, i + MAXP > body.length ? body.length : i + MAXP)));
      }
      ws.add(frame(0x02, sid, []));
    } catch (e) {
      ws.add(jsonEncode({'type': 'res', 'streamId': sid, 'status': 502,
        'headers': {'Content-Type': ['text/plain']}}));
      ws.add(frame(0x01, sid, utf8.encode('etunnel: local service error: $e\n')));
      ws.add(frame(0x02, sid, []));
    }
  }

  void close() {
    ws.add(jsonEncode({'type': 'bye', 'reason': 'client closed'}));
    ws.close();
  }
}

class _StreamBuf {
  final Map<String, dynamic> req;
  final BytesBuilder body = BytesBuilder();
  _StreamBuf(this.req);
}

Future<void> main(List<String> args) async {
  const String kDefaultServer = String.fromEnvironment(
        'ETUNNEL_DEFAULT_SERVER',
        defaultValue: 'wss://t.ggcode.uk/tunnel');
    final server = Platform.environment['ETUNNEL_SERVER'] ?? kDefaultServer;
  final local = args.isNotEmpty ? args[0] : Platform.environment['ETUNNEL_LOCAL'] ?? '3000';
  try {
    final t = await Etunnel.connect(server, local,
        token: Platform.environment['ETUNNEL_TOKEN'],
        subdomain: Platform.environment['ETUNNEL_SUBDOMAIN']);
    print('TUNNEL_URL: ${t.url}');
  } catch (e) {
    stderr.writeln('etunnel: $e');
    exit(1);
  }
  await Completer<void>().future; // run forever
}
