// Minimal etunnel example (Dart).
//   dart run example/basic.dart 3000
import 'dart:io';
import '../etunnel.dart';

Future<void> main(List<String> args) async {
  final port = args.isNotEmpty ? int.parse(args[0]) : 3000;
  final server = await HttpServer.bind(InternetAddress.loopbackIPv4, port);
  server.listen((req) {
    req.response
      ..headers.contentType = ContentType.text
      ..write('hello from etunnel-example\n');
    req.response.close();
  });
  print('local http://127.0.0.1:$port');

  final t = await Etunnel.connect('wss://t.etun.uk/tunnel', '$port');
  print('public URL: ${t.url}');
}
