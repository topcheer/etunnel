<?php
/**
 * etunnel SDK for PHP (implements RFC 6455 WebSocket by hand over streams —
 * no Composer or curl dependencies; only the openssl extension for wss).
 * Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
 *
 * Run:   php etunnel.php 3000
 *
 * Embed:
 *   $t = Etunnel::connect('wss://t.etun.uk/tunnel', 3000);
 *   echo $t->url;
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

        $hello = ['type' => 'hello', 'proto' => 1, 'client' => 'etunnel-php/1.0'];
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
                'headers' => ['Content-Type' => ['text/plain']]]));
            self::sendBinary($sock, self::frame(0x01, $sid, 'etunnel: local service error: request failed'));
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

    // SIGINT/SIGTERM only flip the flag (and stop reconnecting) when pcntl is
    // available; without it the default disposition terminates the process.
    $stopped = false;
    if (function_exists('pcntl_async_signals')) {
        pcntl_async_signals(true);
        $onSignal = function () use (&$stopped): void {
            $stopped = true;
        };
        pcntl_signal(SIGINT, $onSignal);
        pcntl_signal(SIGTERM, $onSignal);
    }

    $attempt = 0;
    $resume = '';
    while (!$stopped) {
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
            if ($stopped || !$reconnect) {
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
        $until = microtime(true) + $delay;
        while (!$stopped && microtime(true) < $until) {
            usleep(50000);
        }
    }
}
