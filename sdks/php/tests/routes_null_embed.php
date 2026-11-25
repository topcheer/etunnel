<?php
// #430 part 2: the README "Multi-target routing" snippet with local = null.
// Before the fix the first request fataled:
//   Etunnel::serve(): Argument #5 ($local) must be of type string, null given
// Usage: php routes_null_embed.php <port-of-the-/api-backend>
// (the "/" fallback always points at the harness's local service on 3000).
$apiPort = (int)($argv[1] ?? 3001);
$sdkDir = getenv('ET_PHP_SDK_DIR') ?: __DIR__ . '/..';
require rtrim($sdkDir, '/') . '/etunnel.php';

Etunnel::connect(getenv('ETUNNEL_SERVER') ?: 'wss://t.etun.uk/tunnel', null, routes: [
    ['prefix' => '/api', 'addr' => '127.0.0.1:' . $apiPort, 'strip' => true],
    ['prefix' => '/',    'addr' => '127.0.0.1:3000'],
], onUrl: fn (string $url) => print("TUNNEL_URL: $url\n"));
