#!/usr/bin/env perl
# Route table tests (same semantics table as sdks/node/route.js).
# Run: perl tests/route_test.pl
use strict;
use warnings;

my $fails = 0;
sub ck {
    my ($name, $got, $want) = @_;
    if ((defined $got ? "$got" : 'undef') ne (defined $want ? "$want" : 'undef')) {
        $fails++;
        print "FAIL $name: got " . (defined $got ? $got : 'undef') . " want " . (defined $want ? $want : 'undef') . "\n";
    }
}

# Load inside a sub so the SDK's `unless (caller)` guard skips its main loop.
sub _load_sdk {
    my $dir = ($0 =~ m{^(.*)/}) ? $1 : '.';
    do "./$dir/../etunnel.pl" or die "load etunnel.pl: $@\n";
}
_load_sdk();

sub rt {
    my ($prefix, $addr, $strip) = @_;
    return { prefix => $prefix, addr => $addr, strip => $strip ? 1 : 0 };
}
sub msg {
    my ($body) = @_;
    my $m = eval { $body->(); return 'accepted' }; # unreachable return keeps msg
    return $@ ? ($@ =~ /(etunnel: route\[\d+\]: [^\n]+)/ ? $1 : 'died') : 'accepted';
}

my $rs = normalize_routes([rt('/', 'fe:1'), rt('/api', 'be:1'), rt('/api/v2', 'be2:1')]);
my ($a) = route_for($rs, '/apiv2');  ck('/apiv2 -> /', $a, 'fe:1');
($a) = route_for($rs, '/apiary');    ck('/apiary -> /', $a, 'fe:1');
($a) = route_for($rs, '/api');       ck('/api exact', $a, 'be:1');
($a) = route_for($rs, '/api/u');     ck('/api/u', $a, 'be:1');
($a) = route_for($rs, '/api/v2/x?q'); ck('longest wins', $a, 'be2:1');

my $ss = normalize_routes([rt('/api', 'be:1', 1), rt('/', 'fe:1', 1)]);
my (undef, $p) = route_for($ss, '/api/u?x=1&y=2'); ck('strip keeps query', $p, '/u?x=1&y=2');
(undef, $p) = route_for($ss, '/api'); ck('exact strip -> /', $p, '/');
(undef, $p) = route_for($ss, '/app?c=1'); ck('strip root prefix = original', $p, '/app?c=1');

my $one = normalize_routes([rt('/api', 'be:1')]);
my @nomatch = route_for($one, '/other');
ck('no match -> empty', scalar(@nomatch), 0);

ck('addr required', (msg(sub { normalize_routes([rt('/', '')]) }) =~ /addr is required/) ? 1 : 0, 1);
ck('must start with /', (msg(sub { normalize_routes([rt('api', 'a:1')]) }) =~ /must start with/) ? 1 : 0, 1);
ck('duplicate', (msg(sub { normalize_routes([rt('/api', 'a:1'), rt('/api/', 'a:2')]) }) =~ /duplicate/) ? 1 : 0, 1);

if ($fails == 0) { print "perl route tests: PASS\n"; exit 0 }
print "perl route tests: FAIL ($fails)\n";
exit 1;
