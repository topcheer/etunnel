#!/bin/sh
# Minimal etunnel example (C SDK).
# The C SDK is a single-file CLI - the example is its real usage: build it with
# the project Makefile (pkg-config: libwebsockets, libcurl, json-c, openssl),
# then expose the local service on :3000 (argv[1] / ETUNNEL_LOCAL overrides).
#
#   sh sdks/c/example/basic.sh            # local port 3000
#   sh sdks/c/example/basic.sh 8080       # local port 8080
#   ETUNNEL_SERVER=wss://t.example.com/tunnel sh sdks/c/example/basic.sh
set -e
cd "$(dirname "$0")/.."
make
exec ./etunnel "${1:-3000}"
