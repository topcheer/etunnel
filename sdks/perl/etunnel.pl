#!/usr/bin/env perl
# etunnel SDK for Perl (core modules only: IO::Socket::SSL, Digest::SHA,
# MIME::Base64, JSON::PP). Implements the WebSocket client (RFC 6455) and the
# etunnel protocol by hand. Buffer-based (bodies buffered per stream).
#
# Run: perl etunnel.pl 3000
use strict;
use warnings;
use threads;
use threads::shared;
use IO::Socket::SSL;
use IO::Socket::INET;
use Digest::SHA qw(sha1_base64);
use MIME::Base64;
use JSON::PP;
use Errno qw(EAGAIN EWOULDBLOCK);
use Time::HiRes qw(time);

my $MAXP = 32768;
my $JSON = JSON::PP->new->utf8;
my $DEBUG = $ENV{ETUNNEL_DEBUG} ? 1 : 0;
sub dbg { print STDERR @_ if $DEBUG }

# --- WebSocket client --------------------------------------------------------

sub ws_connect {
    my ($url) = @_;
    my ($scheme, $host, $port, $path) = $url =~ m{^(wss?)://([^/:]+)(?::(\d+))?(/.*)$}
        or die "bad url $url\n";
    $port //= ($scheme eq 'wss' ? 443 : 80);
    my $sock;
    if ($scheme eq 'wss') {
        $sock = IO::Socket::SSL->new(
            PeerHost => $host, PeerPort => $port,
            SSL_verify_mode => SSL_VERIFY_PEER,
            SSL_hostname => $host,
        ) or die "tls connect: $!\n";
    } else {
        $sock = IO::Socket::INET->new(PeerHost => $host, PeerPort => $port, Proto => 'tcp')
            or die "connect: $!\n";
    }
    binmode($sock);

    my $key = encode_base64(join('', map { chr(int(rand(256))) } 1..16), '');
    my $req = "GET $path HTTP/1.1\r\nHost: $host\r\nUpgrade: websocket\r\n"
        . "Connection: Upgrade\r\nSec-WebSocket-Key: $key\r\n"
        . "Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: etunnel-v1\r\n\r\n";
    syswrite($sock, $req);
    my $resp = '';
    while (1) {
        my $n = sysread($sock, my $buf, 4096);
        die "handshake read: $!\n" unless defined $n;
        die "handshake eof\n" unless $n;
        $resp .= $buf;
        last if $resp =~ /\r\n\r\n/;
    }
    die "websocket handshake rejected: " . substr($resp, 0, 60) . "\n"
        unless $resp =~ /^HTTP\/1\.1 101/;
    return { sock => $sock, host => $host, port => $port };
}

my $send_lock : shared;

sub ws_send_text {
    my ($ws, $payload) = @_;
    ws_send_frame($ws, 0x1, $payload);
}

sub ws_send_binary {
    my ($ws, $payload) = @_;
    ws_send_frame($ws, 0x2, $payload);
}

# client frames must be masked (RFC 6455 §5.3)
sub ws_send_frame {
    my ($ws, $opcode, $payload) = @_;
    my @mask = map { int(rand(256)) } 1..4;
    my $masked = '';
    for my $i (0 .. length($payload) - 1) {
        $masked .= substr($payload, $i, 1) ^ chr($mask[$i % 4]);
    }
    my $len = length($payload);
    my $hdr = chr(0x80 | $opcode);
    if ($len < 126)    { $hdr .= chr(0x80 | $len); }
    elsif ($len < 65536) { $hdr .= chr(0x80 | 126) . pack('n', $len); }
    else               { $hdr .= chr(0x80 | 127) . pack('Q>', $len); }
    { lock($send_lock); syswrite($ws->{sock}, $hdr . pack('C*', @mask) . $masked); }
}

# returns (opcode, payload); dies on close/error
sub ws_recv {
    my ($ws) = @_;
    my $sock = $ws->{sock};
    my $read_hdr = sub {
        my $got = '';
        while (length($got) < 2) {
            my $n = sysread($sock, my $b, 2 - length($got));
            die "eof" unless $n;
            $got .= $b;
        }
        return $got;
    };
    while (1) {
        my $h = $read_hdr->();
        my $fin = ord(substr($h, 0, 1)) & 0x80;
        my $opcode = ord(substr($h, 0, 1)) & 0x0F;
        my $masked = ord(substr($h, 1, 1)) & 0x80;
        my $len = ord(substr($h, 1, 1)) & 0x7F;
        if ($len == 126) {
            my $b = ''; $b .= read_exact($sock, 2 - length($b)) while length($b) < 2;
            $len = unpack('n', $b);
        } elsif ($len == 127) {
            my $b = read_exact($sock, 8);
            $len = unpack('Q>', $b);
        }
        my $mask = $masked ? read_exact($sock, 4) : '';
        my $payload = $len ? read_exact($sock, $len) : '';
        if ($masked) {
            my $out = '';
            $out .= substr($payload, $_, 1) ^ substr($mask, $_ % 4, 1) for 0 .. length($payload) - 1;
            $payload = $out;
        }
        if ($opcode == 0x9) {          # ping -> pong
            ws_send_frame($ws, 0xA, $payload);
            next;
        } elsif ($opcode == 0xA) {     # pong
            next;
        } elsif ($opcode == 0x8) {     # close
            die "closed\n";
        }
        return ($opcode, $payload);    # 0x1 text / 0x2 binary (no fragmentation expected)
    }
}

sub read_exact {
    my ($sock, $n) = @_;
    my $got = '';
    while (length($got) < $n) {
        my $r = sysread($sock, my $b, $n - length($got));
        die "eof" unless $r;
        $got .= $b;
    }
    return $got;
}

# --- etunnel protocol --------------------------------------------------------

sub frame {
    my ($kind, $sid, $payload) = @_;
    return pack('C N N', $kind, $sid, length($payload)) . $payload;
}

sub local_request {
    my ($host, $port, $method, $path, $headers, $body) = @_;
    my %skip = map { lc($_) => 1 } qw(connection transfer-encoding keep-alive
        upgrade proxy-connection te trailer content-length host);
    my @hdrs;
    for my $k (sort keys %$headers) {
        next if $skip{lc $k};
        my $v = $headers->{$k};
        if (ref $v eq 'ARRAY') { push @hdrs, "$k: $_" for @$v; }
        else { push @hdrs, "$k: $v"; }
    }
    my $cl = length($body || '');
    my $req = "$method $path HTTP/1.1\r\nHost: $host:$port\r\n"
        . join("\r\n", @hdrs) . "\r\n"
        . "Content-Length: $cl\r\nConnection: close\r\n\r\n" . ($body || '');
    my $sock = IO::Socket::INET->new(PeerHost => $host, PeerPort => $port, Proto => 'tcp')
        or die "local connect: $!\n";
    syswrite($sock, $req);
    shutdown($sock, 1);
    my $resp = '';
    while (1) {
        my $n = sysread($sock, my $b, 65536);
        last unless $n;
        $resp .= $b;
    }
    close $sock;
    my ($head, $body_out) = split(/\r\n\r\n/, $resp, 2);
    my ($status) = $head =~ /^HTTP\/[\d.]+\s+(\d+)/;
    my %rh;
    for my $line (split(/\r\n/, $head)) {
        next unless $line =~ /^([^:]+):\s*(.*)$/;
        my ($k, $v) = ($1, $2);
        $rh{$k} = [grep { defined } @{$rh{$k} || []}, $v];
    }
    # trim body to Content-Length when present
    my $clv = $rh{'Content-Length'} || $rh{'content-length'};
    if ($clv && @{$clv}) {
        my $want = $clv->[0];
        $body_out = substr($body_out, 0, $want) if defined $body_out;
    }
    return ($status || 502, \%rh, defined($body_out) ? $body_out : '');
}

# Themed 502 page shown when the local app is down (same HTML as the other SDKs).
# Single-quoted heredocs: the CSS @media/@keyframes must not be interpolated.
sub local_error_page {
    my ($msg) = @_;
    my $esc = defined($msg) && length($msg) ? $msg : 'error';
    $esc =~ s/&/&amp;/g;
    $esc =~ s/</&lt;/g;
    $esc =~ s/>/&gt;/g;
    my $page = <<'EOHTML';
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
EOHTML
    my ($pa, $pb) = split(/<!--MSG-->/, $page, 2);
    return $pa . $esc . $pb;
}

sub etunnel_connect {
    my ($server, $local, $opts) = @_;
    $opts ||= {};
    my ($lhost, $lport) = $local =~ /^([^:]+):(\d+)$/ or die "bad local $local\n";
    my $ws = ws_connect($server);
    ws_send_text($ws, $JSON->encode({
        type => 'hello', proto => 1,
        ($opts->{subdomain} ? (subdomain => $opts->{subdomain}) : ()),
        ($opts->{token} ? (token => $opts->{token}) : ()),
        ($opts->{resume} ? (resume => $opts->{resume}) : ()),
        client => 'etunnel-perl/1.0',
    }));
    my $info;
    while (1) {
        my ($op, $payload) = ws_recv($ws);
        next unless $op == 0x1;
        my $m = $JSON->decode($payload);
        if ($m->{type} eq 'welcome') { $info = $m; last; }
        if ($m->{type} eq 'error') { die "etunnel: $m->{code}: $m->{message}\n"; }
    }
    my %streams;
    my %state : shared = (closed => 0);

    threads->create(sub {
        $SIG{PIPE} = 'IGNORE';
        while (1) {
            my ($op, $payload) = eval { ws_recv($ws) };
            if ($@) { dbg "reader exit: $@"; last; }
            if ($op == 0x1) {
                my $m = eval { $JSON->decode($payload) };
                unless ($m) { dbg "text-notjson len=" . length($payload) . "\n"; next; }
                dbg "text type=" . ($m->{type} // '?') . " sid=" . ($m->{streamId} // '-') . "\n";
                my $type = $m->{type} || '';
                if ($type eq 'req') {
                    $streams{ $m->{streamId} } = { req => $m, body => '' };
                } elsif ($type eq 'ping') {
                    eval { ws_send_text($ws, $JSON->encode({ type => 'pong', ts => $m->{ts} // 0 })) };
                } elsif ($type eq 'bye') {
                    $state{closed} = 1;
                    last;
                }
            } else {
                next unless length($payload) >= 9;
                my ($kind, $sid, $n) = unpack('C N N', substr($payload, 0, 9));
                dbg "bin kind=$kind sid=$sid len=$n\n";
                my $data = substr($payload, 9, $n);
                my $st = $streams{$sid} or next;
                if ($kind == 0x01) { $st->{body} .= $data; }
                elsif ($kind == 0x02) {
                    delete $streams{$sid};
                    my $sidf = $sid;
                    my $reqf = $st->{req};
                    my $bodyf = $st->{body};
                    threads->create(sub {
                        my ($status, $rh, $out) = eval {
                            local_request($lhost, $lport, $reqf->{method} || 'GET',
                                $reqf->{path} || '/', $reqf->{headers} || {}, $bodyf);
                        };
                        if ($@) {
                            eval {
                                ws_send_text($ws, $JSON->encode({ type => 'res', streamId => $sidf,
                                    status => 502, headers => { 'Content-Type' => ['text/html; charset=utf-8'] } }));
                                ws_send_binary($ws, frame(0x01, $sidf, local_error_page($@)));
                                ws_send_binary($ws, frame(0x02, $sidf, ''));
                            };
                            return;
                        }
                        eval {
                            ws_send_text($ws, $JSON->encode({ type => 'res', streamId => $sidf,
                                status => $status + 0, headers => $rh }));
                            for (my $i = 0; $i < length($out); $i += $MAXP) {
                                ws_send_binary($ws, frame(0x01, $sidf, substr($out, $i, $MAXP)));
                            }
                            ws_send_binary($ws, frame(0x02, $sidf, ''));
                        };
                    })->detach;
                } elsif ($kind == 0x03) {
                    delete $streams{$sid};
                }
            }
        }
        $state{closed} = 1;
        close $ws->{sock};
    })->detach;

    return {
        url => $info->{url},
        resumeToken => ($info->{resumeToken} // ''),
        state => \%state,
        close => sub {
            return if $state{closed};
            eval { ws_send_text($ws, $JSON->encode({ type => 'bye', reason => 'client closed' })) };
            close $ws->{sock};
        },
    };
}

# --- main ----------------------------------------------------------------------
$| = 1; # unbuffered stdout

my $server = $ENV{ETUNNEL_SERVER} || 'wss://t.etun.uk/tunnel';
my $local = $ARGV[0] || $ENV{ETUNNEL_LOCAL} || '3000';
$local = "127.0.0.1:$local" unless $local =~ /:/;
my $reconnect = ($ENV{ETUNNEL_RECONNECT} // '1') ne '0';
my $stopped : shared = 0;
$SIG{INT} = $SIG{TERM} = sub { $stopped = 1 };

# 500ms doubling, capped at 30s, +-20% jitter.
my $backoff = sub {
    my $base = 0.5 * (2 ** $_[0]);
    $base = 30 if $base > 30;
    return $base * (0.8 + rand() * 0.4);
};
my $nap = sub {
    my $deadline = time() + $_[0];
    while (!$stopped) {
        my $left = $deadline - time();
        last if $left <= 0;
        select(undef, undef, undef, $left > 0.2 ? 0.2 : $left);
    }
};

my $attempt = 0;
my $resume = ''; # server-signed token: reclaim the same subdomain on reconnect
while (!$stopped) {
    my $t = eval {
        etunnel_connect($server, $local, {
            token => $ENV{ETUNNEL_TOKEN}, subdomain => $ENV{ETUNNEL_SUBDOMAIN},
            resume => $resume,
        });
    };
    if (!$t) {
        my $err = $@ || "connect failed\n";
        chomp $err;
        print STDERR "etunnel: $err\n";
        exit 1 unless $reconnect;
    } else {
        $resume = $t->{resumeToken} if $t->{resumeToken};
        print "TUNNEL_URL: $t->{url}\n";
        $attempt = 0;
        unless ($reconnect) {
            sleep 1 while !$stopped;
            eval { $t->{close}->() };
            last;
        }
        # the reader thread raises the shared flag when the socket goes away
        select(undef, undef, undef, 0.25) while !$stopped && !$t->{state}{closed};
        if ($stopped) { eval { $t->{close}->() }; last; }
        print STDERR "etunnel: connection lost\n";
    }
    my $delay = $backoff->($attempt);
    $attempt++;
    last if $stopped;
    printf STDERR "etunnel: reconnecting in %.1fs\n", $delay;
    $nap->($delay);
}
