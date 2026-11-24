#!/bin/sh
# Minimal etunnel example (C++ SDK).
# The C++ SDK is a single-file CLI - the example is its real usage: build it
# with CMake (websocketpp + asio/Boost.Asio + libcurl + OpenSSL), then expose
# the local service on :3000 (argv[1] / ETUNNEL_LOCAL overrides).
#
#   sh sdks/cpp/example/basic.sh          # local port 3000
#   sh sdks/cpp/example/basic.sh 8080     # local port 8080
#   ETUNNEL_SERVER=wss://t.example.com/tunnel sh sdks/cpp/example/basic.sh
#
# CMAKE_ARGS (optional) is appended to the configure step - e.g. on macOS with
# Homebrew's asio > 1.13, which websocketpp 0.8.x cannot use (the openssl@3 keg
# is picked automatically since 1.7.36):
#   CMAKE_ARGS="-DASIO_INCLUDE_DIR=/path/to/asio-1.13/asio/include" sh sdks/cpp/example/basic.sh
set -e
cd "$(dirname "$0")/.."
# shellcheck disable=SC2086  # CMAKE_ARGS is meant to word-split
cmake -B build -DCMAKE_BUILD_TYPE=Release ${CMAKE_ARGS:-}
cmake --build build --target etunnel
exec ./build/etunnel "${1:-3000}"
