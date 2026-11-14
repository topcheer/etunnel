#!/usr/bin/env perl
# Minimal etunnel example (Perl, core modules only).
#   perl example/basic.pl 3000
use strict;
use warnings;
use lib '.';
require './etunnel.pl';

my $port = $ARGV[0] || 3000;
# env (ETUNNEL_SERVER/LOCAL/TOKEN/SUBDOMAIN) is read inside etunnel_connect.
my $t = etunnel_connect("wss://t.etun.uk/tunnel", $port);
print "public URL: " . ($t->{url} // "?") . "\n";

while (1) {
    etunnel_pump($t) if defined &etunnel_pump;
    sleep 1;
}
