use strict;
use warnings;
use ExtUtils::MakeMaker;

WriteMakefile(
    NAME         => 'etunnel',
    VERSION      => '1.6.4',
    ABSTRACT     => 'Expose local services to the internet in one line of code: instant public HTTPS URLs with automatic TLS, through NAT and firewalls, no signup.',
    AUTHOR       => 'etunnel authors <etunnel@example.com>',
    LICENSE      => 'mit',
    EXE_FILES    => ['etunnel.pl'],
    PREREQ_PM    => {
        # core modules only: IO::Socket::SSL, JSON::PP, HTTP::Tiny
    },
    META_MERGE   => {
        resources => {
            repository => 'https://github.com/ggcode/etunnel',
            homepage   => 'https://github.com/ggcode/etunnel',
        },
        keywords => [qw(tunnel reverse-proxy webhook localhost)],
    },
);
