<?php
// Route table tests (same semantics table as sdks/node/route.js).
// Run: php tests/route_test.php
require __DIR__ . '/../etunnel.php';

$fails = 0;
function ck(string $name, $got, $want): void {
    global $fails;
    if ($got !== $want) {
        $fails++;
        fwrite(STDERR, "FAIL $name: got " . var_export($got, true) . " want " . var_export($want, true) . "\n");
    }
}
function msg(callable $body): string {
    try { $body(); return 'accepted'; }
    catch (InvalidArgumentException $e) { return $e->getMessage(); }
}

$rs = Etunnel::normalizeRoutes([
    ['prefix' => '/', 'addr' => 'fe:1'],
    ['prefix' => '/api', 'addr' => 'be:1'],
    ['prefix' => '/api/v2', 'addr' => 'be2:1'],
]);
ck('/apiv2 -> /', Etunnel::routeFor($rs, '/apiv2')['addr'], 'fe:1');
ck('/apiary -> /', Etunnel::routeFor($rs, '/apiary')['addr'], 'fe:1');
ck('/api exact', Etunnel::routeFor($rs, '/api')['addr'], 'be:1');
ck('/api/u', Etunnel::routeFor($rs, '/api/u')['addr'], 'be:1');
ck('longest wins', Etunnel::routeFor($rs, '/api/v2/x?q')['addr'], 'be2:1');

$ss = Etunnel::normalizeRoutes([
    ['prefix' => '/api', 'addr' => 'be:1', 'strip' => true],
    ['prefix' => '/', 'addr' => 'fe:1', 'strip' => true],
]);
ck('strip keeps query', Etunnel::routeFor($ss, '/api/u?x=1&y=2')['path'], '/u?x=1&y=2');
ck('exact strip -> /', Etunnel::routeFor($ss, '/api')['path'], '/');
ck('strip root prefix = original', Etunnel::routeFor($ss, '/app?c=1')['path'], '/app?c=1');

$one = Etunnel::normalizeRoutes([['prefix' => '/api', 'addr' => 'be:1']]);
ck('no match -> null', Etunnel::routeFor($one, '/other'), null);

ck('addr required', str_contains(msg(fn() => Etunnel::normalizeRoutes([['prefix' => '/', 'addr' => '']])), 'addr is required'), true);
ck('must start with /', str_contains(msg(fn() => Etunnel::normalizeRoutes([['prefix' => 'api', 'addr' => 'a:1']])), "must start with"), true);
ck('duplicate', str_contains(msg(fn() => Etunnel::normalizeRoutes([['prefix' => '/api', 'addr' => 'a:1'], ['prefix' => '/api/', 'addr' => 'a:2']])), 'duplicate'), true);

if ($fails === 0) { echo "php route tests: PASS\n"; exit(0); }
echo "php route tests: FAIL ($fails)\n";
exit(1);
