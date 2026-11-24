// CLI entry for `dart run etunnel.dart 3000` (kept at the package root for the
// single-file habit and for tests/proto_gate.sh). The implementation lives in
// lib/etunnel.dart so `import 'package:etunnel/etunnel.dart'` resolves (#417 E26).
export 'lib/etunnel.dart';

import 'lib/etunnel.dart';

Future<void> main(List<String> args) => run(args);
