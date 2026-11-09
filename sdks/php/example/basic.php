<?php
// Minimal etunnel example: serve a local HTTP page through a public tunnel.
//   php -S 127.0.0.1:3000 example/index.php   # in one shell (a plain page)
//   php example/basic.php 3000                # in another
require __DIR__ . '/../etunnel.php';

$port = (int)($argv[1] ?? 3000);
$t = Etunnel::connect('wss://t.etun.uk/tunnel', $port, null, null, function ($url) {
    fwrite(STDERR, "public URL: " . $url . "\n");
});
fwrite(STDERR, "tunnel open; Ctrl-C to stop\n");
while (true) {
    usleep(1000000);
}
