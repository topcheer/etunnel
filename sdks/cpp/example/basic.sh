#!/bin/sh
# Minimal etunnel example (C++ SDK).
# The C++ SDK is a single-file CLI - the example is its real usage:
set -e
cd "$(dirname "$0")/.."
c++ -std=c++17 -O2 -o /tmp/etunnel-cpp etunnel.cpp -lssl -lcrypto

# serve a local service on :3000 through a public tunnel
/tmp/etunnel-cpp 3000
