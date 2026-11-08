use strict;
use warnings;
use ExtUtils::MakeMaker;

WriteMakefile(
    NAME         => 'etunnel',
    VERSION      => '1.0.0',
    ABSTRACT     => 'Expose a local service through an etunnel reverse-tunnel server',
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
