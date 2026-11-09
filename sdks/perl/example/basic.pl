#!/usr/bin/env perl
# Minimal etunnel example (Perl, core modules only).
#   perl example/basic.pl 3000
use strict;
use warnings;
use lib '.';
require './etunnel.pl';

my $port = $ARGV[0] || 3000;
set_defaults();   # reads ETUNNEL_* env (server/local/token/subdomain)
my $t = etunnel_connect("wss://t.etun.uk/tunnel", $port);
print "public URL: " . ($t->{url} // "?") . "\n";

while (1) {
    etunnel_pump($t) if defined &etunnel_pump;
    sleep 1;
}
