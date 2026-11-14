// Route table tests (same semantics table as sdks/node/route.js).
// Run: dart run tests/route_test.dart
import 'dart:io';
import '../etunnel.dart';

var fails = 0;
void ck(String name, Object? got, Object? want) {
  if (got.toString() != want.toString()) {
    fails++;
    stderr.writeln('FAIL $name: got $got want $want');
  }
}

String msg(void Function() body) {
  try {
    body();
    return 'accepted';
  } on ArgumentError catch (e) {
    return e.message.toString();
  }
}

void main() {
  final rs = Etunnel.normalizeRoutes([
    const Route('/', 'fe:1'),
    const Route('/api', 'be:1'),
    const Route('/api/v2', 'be2:1'),
  ]);
  ck('/apiv2 -> /', Etunnel.routeFor(rs, '/apiv2')?.addr, 'fe:1');
  ck('/apiary -> /', Etunnel.routeFor(rs, '/apiary')?.addr, 'fe:1');
  ck('/api exact', Etunnel.routeFor(rs, '/api')?.addr, 'be:1');
  ck('/api/u', Etunnel.routeFor(rs, '/api/u')?.addr, 'be:1');
  ck('longest wins', Etunnel.routeFor(rs, '/api/v2/x?q')?.addr, 'be2:1');

  final ss = Etunnel.normalizeRoutes([
    const Route('/api', 'be:1', strip: true),
    const Route('/', 'fe:1', strip: true),
  ]);
  ck('strip keeps query', Etunnel.routeFor(ss, '/api/u?x=1&y=2')?.path, '/u?x=1&y=2');
  ck('exact strip -> /', Etunnel.routeFor(ss, '/api')?.path, '/');
  ck('strip root prefix = original', Etunnel.routeFor(ss, '/app?c=1')?.path, '/app?c=1');

  final one = Etunnel.normalizeRoutes([const Route('/api', 'be:1')]);
  ck('no match -> null', Etunnel.routeFor(one, '/other'), null);

  ck('addr required',
      msg(() => Etunnel.normalizeRoutes([const Route('/', '')])).contains('addr is required'), true);
  ck('must start with /',
      msg(() => Etunnel.normalizeRoutes([const Route('api', 'a:1')])).contains("must start with"), true);
  ck('duplicate',
      msg(() => Etunnel.normalizeRoutes([const Route('/api', 'a:1'), const Route('/api/', 'a:2')]))
          .contains('duplicate'), true);

  if (fails == 0) {
    print('dart route tests: PASS');
  } else {
    stderr.writeln('dart route tests: FAIL ($fails)');
    exit(1);
  }
}
