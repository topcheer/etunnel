// E2E harness: run by tests/test_login.sh against the shared Go loginserver
// fixture (FX_ADDR env). Prints CRED:token:subs:plan on success.
import 'dart:async';
import 'dart:io';
import '../etunnel.dart';

Future<void> main() async {
  final c = await etunnelLogin('http://' + (Platform.environment['FX_ADDR'] ?? ''),
      openBrowser: (u) async {
        // fire-and-forget: awaiting curl would deadlock the loopback
        // handler (curl waits on the grant response, which waits on us).
        unawaited(Process.start('curl', ['-s', '-o', '/dev/null', u]));
      },
      timeout: const Duration(seconds: 30));
  print('CRED:${c.apiToken}:${c.subdomains.join(",")}:${c.plan}');
}
