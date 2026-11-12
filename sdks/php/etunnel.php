<?php
/**
 * etunnel SDK for PHP (implements RFC 6455 WebSocket by hand over streams —
 * no Composer or curl dependencies; only the openssl extension for wss).
 * Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
 *
 * Run:   php etunnel.php 3000
 *
 * Embed (connect() blocks, serving until the tunnel ends - the URL arrives
 * via the $onUrl callback, like the CLI below does):
 *   Etunnel::connect('wss://t.etun.uk/tunnel', 3000, null, null,
 *       function (string $url): void { echo "TUNNEL_URL: $url\n"; });
 */

const MAXP = 32768;

final class Etunnel
{
    public readonly string $url;
    /** Server-signed token: pass back as $resume to reclaim the subdomain. */
    public static string $lastResumeToken = '';
    private $sock;

    private function __construct($sock, string $url)
    {
        $this->sock = $sock;
        $this->url = $url;
    }

    public static function connect(string $server, $local, ?string $token = null, ?string $subdomain = null, ?callable $onUrl = null, ?string $resume = null): self
    {
        if (is_numeric($local)) {
            $local = '127.0.0.1:' . $local;
        }
        $p = parse_url($server);
        $tls = ($p['scheme'] ?? '') === 'wss';
        $host = $p['host'];
        $port = $p['port'] ?? ($tls ? 443 : 80);
        $path = ($p['path'] ?? '/') . (isset($p['query']) ? '?' . $p['query'] : '');

        $ctx = stream_context_create(['ssl' => ['verify_peer' => true, 'verify_peer_name' => true, 'SNI_enabled' => true]]);
        $remote = ($tls ? 'ssl://' : 'tcp://') . $host . ':' . $port;
        $sock = @stream_socket_client($remote, $errno, $errstr, 15, STREAM_CLIENT_CONNECT, $ctx);
        if (!$sock) {
            throw new RuntimeException("etunnel: connect: $errstr");
        }
        stream_set_timeout($sock, 3600);

        $key = base64_encode(random_bytes(16));
        $req = "GET $path HTTP/1.1\r\nHost: $host\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
            . "Sec-WebSocket-Key: $key\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: etunnel-v1\r\n\r\n";
        fwrite($sock, $req);
        $resp = '';
        while (substr($resp, -4) !== "\r\n\r\n") {
            $chunk = fgets($sock, 4096);
            if ($chunk === false) {
                throw new RuntimeException('etunnel: handshake failed');
            }
            $resp .= $chunk;
        }
        if (!str_starts_with($resp, 'HTTP/1.1 101')) {
            throw new RuntimeException('etunnel: websocket rejected');
        }

        $hello = ['type' => 'hello', 'proto' => 1, 'client' => 'etunnel-php/1.4.0'];
        if ($token) $hello['token'] = $token;
        if ($subdomain) $hello['subdomain'] = $subdomain;
        if ($resume !== null && $resume !== '') $hello['resume'] = $resume;
        self::sendText($sock, json_encode($hello));

        $streams = [];
        $url = null;
        while (true) {
            [$opcode, $payload] = self::recv($sock);
            if ($opcode === 0x1) {
                $m = json_decode($payload, true);
                if (($m['type'] ?? '') === 'welcome') {
                    $url = $m['url'];
                    self::$lastResumeToken = (string)($m['resumeToken'] ?? '');
                    break;
                }
                if (($m['type'] ?? '') === 'error') {
                    throw new RuntimeException("etunnel: {$m['code']}: {$m['message']}");
                }
            }
        }

        $t = new self($sock, $url);
        // PHP is single-threaded: report the URL immediately, then serve.
        if ($onUrl !== null) {
            $onUrl($url);
        }
        // Reader loop (process-level; handle messages sequentially).
        while (true) {
            [$opcode, $payload] = self::recv($sock);
            if ($opcode === null) {
                return $t;
            }
            if ($opcode === 0x1) {
                $m = json_decode($payload, true);
                $type = $m['type'] ?? '';
                if ($type === 'req') {
                    $streams[$m['streamId']] = ['req' => $m, 'body' => ''];
                } elseif ($type === 'ping') {
                    self::sendText($sock, json_encode(['type' => 'pong', 'ts' => $m['ts'] ?? 0]));
                } elseif ($type === 'bye') {
                    return $t;
                }
            } elseif ($opcode === 0x2 && strlen($payload) >= 9) {
                $kind = ord($payload[0]);
                $sid = unpack('N', substr($payload, 1, 4))[1];
                $n = unpack('N', substr($payload, 5, 4))[1];
                $data = substr($payload, 9, $n);
                if (!isset($streams[$sid])) {
                    continue;
                }
                if ($kind === 0x01) {
                    $streams[$sid]['body'] .= $data;
                } elseif ($kind === 0x02) {
                    $st = $streams[$sid];
                    unset($streams[$sid]);
                    self::serve($sock, $st['req'], $st['body'], $sid, $local);
                } elseif ($kind === 0x03) {
                    unset($streams[$sid]);
                }
            }
        }
        return $t;
    }

    private static function serve($sock, array $req, string $body, int $sid, string $local): void
    {
        $method = $req['method'] ?? 'GET';
        $path = $req['path'] ?? '/';
        $headers = '';
        foreach (($req['headers'] ?? []) as $k => $vs) {
            if (in_array(strtolower($k), ['connection', 'transfer-encoding', 'keep-alive', 'upgrade',
                'proxy-connection', 'te', 'trailer', 'content-length', 'host'], true)) {
                continue;
            }
            foreach ((array) $vs as $v) {
                $headers .= "$k: $v\r\n";
            }
        }
        $opts = ['http' => [
            'method' => $method,
            'header' => trim($headers),
            'ignore_errors' => true,
            'timeout' => 120,
        ]];
        if ($method !== 'GET' && $method !== 'HEAD' && $body !== '') {
            $opts['http']['content'] = $body;
        }
        $ctx = stream_context_create($opts);
        $raw = @file_get_contents("http://{$local}{$path}", false, $ctx);
        if ($raw === false || !isset($http_response_header)) {
            self::sendText($sock, json_encode(['type' => 'res', 'streamId' => $sid, 'status' => 502,
                'headers' => ['Content-Type' => ['text/html; charset=utf-8']]]));
            self::sendBinary($sock, self::frame(0x01, $sid, self::localErrorPage('request failed')));
            self::sendBinary($sock, self::frame(0x02, $sid, ''));
            return;
        }
        $status = 502;
        $hdrs = [];
        foreach ($http_response_header as $line) {
            if (preg_match('#^HTTP/[\d.]+\s+(\d+)#', $line, $mm)) {
                $status = (int) $mm[1];
                continue;
            }
            [$k, $v] = array_pad(explode(':', $line, 2), 2, '');
            if ($k === '' || $v === '') {
                continue;
            }
            if (!in_array(strtolower($k), ['connection', 'transfer-encoding', 'content-length', 'keep-alive'], true)) {
                $hdrs[trim($k)][] = trim($v);
            }
        }
        $payloadOut = (string) $raw;
        self::sendText($sock, json_encode(['type' => 'res', 'streamId' => $sid, 'status' => $status, 'headers' => $hdrs]));
        for ($i = 0, $n = strlen($payloadOut); $i < $n; $i += MAXP) {
            self::sendBinary($sock, self::frame(0x01, $sid, substr($payloadOut, $i, MAXP)));
        }
        self::sendBinary($sock, self::frame(0x02, $sid, ''));
    }

    // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
    private static function localErrorPage(string $msg): string
    {
        $esc = str_replace(['&', '<', '>'], ['&amp;', '&lt;', '&gt;'], $msg);
        $page = <<<'HTML'
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
<div class="k">error</div><div class="v err"><!--MSG--></div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>
HTML;
        return str_replace('<!--MSG-->', $esc, $page);
    }

    private static function frame(int $kind, int $sid, string $payload): string
    {
        return pack('C', $kind) . pack('N', $sid) . pack('N', strlen($payload)) . $payload;
    }

    private static function sendText($sock, string $s): void
    {
        self::sendFrame($sock, 0x1, $s);
    }

    private static function sendBinary($sock, string $s): void
    {
        self::sendFrame($sock, 0x2, $s);
    }

    private static function sendFrame($sock, int $opcode, string $payload): void
    {
        $mask = random_bytes(4);
        $masked = '';
        $n = strlen($payload);
        for ($i = 0; $i < $n; $i++) {
            $masked .= $payload[$i] ^ $mask[$i % 4];
        }
        $hdr = chr(0x80 | $opcode);
        if ($n < 126) {
            $hdr .= chr(0x80 | $n);
        } elseif ($n < 65536) {
            $hdr .= chr(0x80 | 126) . pack('n', $n);
        } else {
            $hdr .= chr(0x80 | 127) . pack('J', $n);
        }
        fwrite($sock, $hdr . $mask . $masked);
    }

    /** @return array{0:?int,1:?string} opcode+payload, [null,null] on close */
    private static function recv($sock): array
    {
        $read = function (int $n) use ($sock): string {
            $got = '';
            while (strlen($got) < $n) {
                $b = fread($sock, $n - strlen($got));
                if ($b === false || $b === '') {
                    throw new RuntimeException('etunnel: eof');
                }
                $got .= $b;
            }
            return $got;
        };
        try {
            $h = $read(2);
            $opcode = ord($h[0]) & 0x0F;
            $masked = (ord($h[1]) & 0x80) !== 0;
            $len = ord($h[1]) & 0x7F;
            if ($len === 126) {
                $len = unpack('n', $read(2))[1];
            } elseif ($len === 127) {
                $len = unpack('J', $read(8))[1];
            }
            $mask = $masked ? $read(4) : '';
            $payload = $len ? $read($len) : '';
            if ($masked) {
                $out = '';
                for ($i = 0; $i < $len; $i++) {
                    $out .= $payload[$i] ^ $mask[$i % 4];
                }
                $payload = $out;
            }
            if ($opcode === 0x8) {
                return [null, null];
            }
            if ($opcode === 0x9) { // ping -> pong
                self::sendFrame($sock, 0xA, $payload);
                return self::recv($sock);
            }
            if ($opcode === 0xA) { // pong
                return self::recv($sock);
            }
            return [$opcode, $payload];
        } catch (RuntimeException) {
            return [null, null];
        }
    }

    public function close(): void
    {
        @self::sendText($this->sock, json_encode(['type' => 'bye', 'reason' => 'client closed']));
        @fclose($this->sock);
    }
}

if (PHP_SAPI === 'cli' && isset($argv[0]) && realpath($argv[0]) === __FILE__) {
    $server = getenv('ETUNNEL_SERVER') ?: 'wss://t.etun.uk/tunnel';
    $local = $argv[1] ?? getenv('ETUNNEL_LOCAL') ?? '3000';
    $reconnect = getenv('ETUNNEL_RECONNECT') !== '0';

    // PHP is single-threaded and blocks inside connect()'s stream read, where a
    // pcntl handler is never dispatched (the EINTR that a signal causes is
    // swallowed by the read) - installing one would make SIGINT/SIGTERM
    // un-killable and the CLI would reconnect forever. Keep the default
    // disposition instead: the process terminates and does not reconnect.
    $attempt = 0;
    $resume = '';
    while (true) {
        try {
            // connect() reports the URL through $onUrl, then serves until the
            // connection ends (bye, close or EOF) and returns.
            Etunnel::connect($server, $local, getenv('ETUNNEL_TOKEN') ?: null, getenv('ETUNNEL_SUBDOMAIN') ?: null,
                function (string $u): void {
                    fwrite(STDOUT, "TUNNEL_URL: {$u}\n");
                    fflush(STDOUT);
                }, $resume);
            if (Etunnel::$lastResumeToken !== '') $resume = Etunnel::$lastResumeToken;
            $attempt = 0;
            if (!$reconnect) {
                break;
            }
            fwrite(STDERR, "etunnel: connection lost\n");
        } catch (RuntimeException $e) {
            fwrite(STDERR, $e->getMessage() . "\n");
            if (!$reconnect) {
                exit(1);
            }
        }

        // 500ms doubling, capped at 30s, +-20% jitter.
        $base = min(0.5 * (2 ** $attempt), 30.0);
        $delay = $base * (0.8 + (mt_rand() / mt_getrandmax()) * 0.4);
        $attempt++;
        fwrite(STDERR, sprintf("etunnel: reconnecting in %.1fs\n", $delay));
        usleep((int) round($delay * 1_000_000));
    }
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
//   $creds = Etunnel::login('https://t.etun.uk');   // opens the browser
//   // -> ['api_token'=>..., 'subdomains'=>[...], 'plan'=>..., 'expires_at'=>0]
// Binds a random 127.0.0.1 port, waits for the one-time code on /callback
// (10-minute budget), exchanges it. Persist the result and pass api_token
// as the $token argument of future Etunnel::connect calls.
final class EtunnelLogin {
    public static function login(string $server, ?callable $openBrowser = null, int $timeout = 600): array {
        $base = rtrim($server, '/');
        if ($base === '') throw new RuntimeException('etunnel: login: server required');

        $sock = @stream_socket_server('tcp://127.0.0.1:0', $errno, $errstr);
        if (!$sock) throw new RuntimeException("etunnel: login: bind loopback failed: $errstr");
        $name = stream_socket_get_name($sock, false);
        $port = (int)substr($name, strrpos($name, ':') + 1);

        // Fire the browser side first, then block on the single callback.
        $start = self::post($base . '/auth/cli/start', ['redirect_uri' => "http://127.0.0.1:{$port}/callback"]);
        if (empty($start['auth_url']) || empty($start['device_code'])) {
            fclose($sock);
            throw new RuntimeException('etunnel: login: empty start payload');
        }
        $open = $openBrowser ?: function (string $u): bool {
            $cmd = PHP_OS_FAMILY === 'Darwin' ? 'open' : (PHP_OS_FAMILY === 'Windows' ? 'start ""' : 'xdg-open');
            return (PHP_OS_FAMILY === 'Windows') ? pclose(popen("start \"\" " . escapeshellarg($u), 'r')) === 0
                                                 : (PHP_OS_FAMILY === 'Darwin' ? @system(escapeshellcmd('open') . ' ' . escapeshellarg($u) . ' >/dev/null 2>&1') !== false
                                                 : @system('xdg-open ' . escapeshellarg($u) . ' >/dev/null 2>&1') !== false);
        };
        $opened = false;
        try { $opened = (bool)$open($start['auth_url']); } catch (Throwable $t) { $opened = false; }
        if (!$opened) fwrite(STDERR, "etunnel: authorize in your browser: {$start['auth_url']}\n");

        $deadline = time() + $timeout;
        $code = '';
        while (time() < $deadline) {
            $client = @stream_socket_accept($sock, 1.0);
            if (!$client) continue;
            $req = fread($client, 4096);
            if (preg_match('#GET /callback\?[^\s]*[?&]code=([^&\s]+)#', $req, $m)) $code = urldecode($m[1]);
            $body = '<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>';
            fwrite($client, "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: " . strlen($body) . "\r\nConnection: close\r\n\r\n" . $body);
            fclose($client);
            if ($code !== '') break;
        }
        fclose($sock);
        if ($code === '') throw new RuntimeException('etunnel: login timed out');

        $creds = self::post($base . '/auth/cli/exchange', ['device_code' => $start['device_code'], 'code' => $code]);
        if (empty($creds['api_token'])) throw new RuntimeException('etunnel: login: no api_token in exchange response');
        return $creds;
    }

    private static function post(string $url, array $payload): array {
        $ctx = stream_context_create(['http' => [
            'method' => 'POST', 'header' => "Content-Type: application/json\r\n",
            'content' => json_encode($payload), 'timeout' => 30, 'ignore_errors' => true,
        ]]);
        $raw = @file_get_contents($url, false, $ctx);
        if ($raw === false) throw new RuntimeException("etunnel: login: request to $url failed");
        $j = json_decode($raw, true);
        if (!is_array($j)) throw new RuntimeException("etunnel: login: bad response from $url: " . substr($raw, 0, 200));
        return $j;
    }
}
