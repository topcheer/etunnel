#!/usr/bin/env python3
"""test_idle_alive.py — #241 regression: an idle tunnel must survive past the
socket timeout.

The bug: websocket.create_connection(timeout=15) leaves the 15s timeout on the
socket for the connection's whole life, so any tunnel quiet for 15s+ hit
WebSocketTimeoutException and closed with "read closed" — every idle tunnel
dropped within 15-45s (the server's first ping only arrives at t=30s).

The fix clears the socket timeout after the welcome exchange.

Method: runs the REAL etunneld binary (built from this tree) and connects the
real Python SDK, then idles for 20s. The production PingInterval is 30s, so no
ping arrives inside the window:

  - fixed code: recv() blocks quietly -> still alive at 20s
  - buggy code: socket timeout fires at ~15s -> on_close("read closed")

Two server facts the test encodes (both from server/server.go):
  * tls-mode=http serves the main mux on -http-addr, not -addr
  * routeByHost only forwards the BASE DOMAIN to that mux; anything else
    (including 127.0.0.1) lands in the visitor proxy and 404s. So the dial
    injects `Host: t.test` via a create_connection wrapper.

  python3 tests/test_idle_alive.py        # expect: PASS (alive at 20s)
  BUG=1 python3 tests/test_idle_alive.py  # re-applies a 3s socket timeout
                                          # after connect -> FAIL (sensitivity)
"""
import os
import socket
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import websocket as _ws  # noqa: E402  (after path insert)
_orig_create = _ws.create_connection

BUG = os.environ.get("BUG") == "1"
HERE = os.path.dirname(os.path.abspath(__file__))
BIN = os.path.abspath(os.path.join(HERE, "..", "..", "..", "etunneld-test-bin"))


_getaddrinfo = socket.getaddrinfo


def _patched_getaddrinfo(host, *a, **kw):
    # The dial URL uses the base domain (routeByHost only forwards it to the
    # main mux); resolve it to the loopback this test listens on. Injecting a
    # Host header instead does not work: websocket-client always adds its own
    # from the URL and the duplicate is rejected with 400.
    if host == "t.test":
        host = "127.0.0.1"
    return _getaddrinfo(host, *a, **kw)


socket.getaddrinfo = _patched_getaddrinfo


_last_ws = []


def _patched_create(url, *a, **kw):
    w = _orig_create(url, *a, **kw)
    _last_ws.append(w)
    return w


_ws.create_connection = _patched_create

import etunnel  # noqa: E402  (imported after the patch so connect() sees it)


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def main() -> int:
    if not os.path.exists(BIN):
        print(f"SKIP: {BIN} not built (run: go build -o {BIN} ./cmd/etunneld)")
        return 0
    port = free_port()  # the plain-HTTP entry; -addr keeps its (unused) TLS role
    admin = free_port()
    accts = os.path.join(tempfile.mkdtemp(), "accts.json")
    srvlog = os.path.join(os.path.dirname(accts), "etd.log")
    proc = subprocess.Popen(
        [BIN, "-domain=t.test", f"-addr=127.0.0.1:{free_port()}",
         f"-http-addr=127.0.0.1:{port}",
         f"-admin-addr=127.0.0.1:{admin}", "-tls-mode=http",
         f"-accounts={accts}", "-max-tunnels-per-ip=0"],
        stdout=open(srvlog, "wb"), stderr=subprocess.STDOUT,
    )
    try:
        deadline = time.time() + 5
        while time.time() < deadline:
            try:
                c = socket.create_connection(("127.0.0.1", port), 0.2)
                c.close()
                break
            except OSError:
                if proc.poll() is not None:
                    print(f"FAIL: etunneld exited rc={proc.returncode}; log:")
                    print(open(srvlog).read()[:2000])
                    return 1
                time.sleep(0.1)

        closes: list = []
        etunnel.connect(
            f"ws://t.test:{port}/tunnel",
            9,  # local port; nothing dials it, the tunnel stays idle
            opts={"on_close": lambda reason: closes.append(reason)},
        )
        if BUG:
            # Sensitivity check: re-apply a persistent socket timeout AFTER
            # connect() returns (the fix's settimeout(None) has already run),
            # simulating the pre-fix state at compressed scale (3s not 15s).
            # Proven to fire: a direct experiment raised
            # WebSocketTimeoutException at exactly 3.0s with this setup.
            _last_ws[-1].settimeout(3)
        time.sleep(20)

        if closes:
            print(f"FAIL: idle tunnel dropped: {closes}")
            return 1
        print("PASS: idle tunnel alive after 20s (past the old 15s socket timeout)")
        return 0
    finally:
        proc.terminate()
        try:
            proc.wait(3)
        except subprocess.TimeoutExpired:
            proc.kill()


if __name__ == "__main__":
    sys.exit(main())
