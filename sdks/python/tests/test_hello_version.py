#!/usr/bin/env python3
"""hello.version + client_outdated: the SDK reports __version__ in hello and
surfaces the server's client_outdated message verbatim (with upgrade URL).

  python3 tests/test_hello_version.py    # expect: PASS
"""
import json
import os
import sys
import threading

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import websocket as _ws  # noqa: E402

OUTDATED = ("etunnel client 1.0.0 is too old; this server runs 9.9.9 and requires "
            "SDK >= 1.7.0. Upgrade: https://github.com/topcheer/etunnel")


class FakeConn:
    def __init__(self, reply):
        self._cv = threading.Condition()
        self._reply = reply
        self.sent = []
        self.closed = False

    def send(self, d):
        self.sent.append(d)
        return None

    def recv(self):
        with self._cv:
            if self._reply is not None:
                r, self._reply = self._reply, None
                return r
            raise ConnectionError("closed")

    def settimeout(self, _t):
        return None

    def close(self):
        with self._cv:
            self.closed = True
            self._cv.notify_all()


def main() -> int:
    import etunnel

    fails = 0

    def ck(name, got, want):
        nonlocal fails
        if got != want:
            fails += 1
            print(f"FAIL {name}: got {got!r} want {want!r}")

    # 1) hello carries version + derived client
    welcome = json.dumps({"type": "welcome", "proto": 1, "url": "https://f.t.test",
                          "hostname": "f.t.test", "subdomain": "f", "plan": "free"})
    fake = FakeConn(welcome)
    _ws.create_connection = lambda *a, **k: fake
    etunnel.connect("ws://fake/tunnel", 3000)
    hello = json.loads(fake.sent[0])
    ck("hello.version === __version__", hello.get("version"), etunnel.__version__)
    ck("hello.version literal", hello.get("version"), "1.7.39")
    ck("client derived", hello.get("client"), f"etunnel-python/{etunnel.__version__}")

    # 2) client_outdated: message (with URL) reaches the caller as RuntimeError
    fake2 = FakeConn(json.dumps({"type": "error", "code": "client_outdated", "message": OUTDATED}))
    _ws.create_connection = lambda *a, **k: fake2
    try:
        etunnel.connect("ws://fake/tunnel", 3000)
        fails += 1
        print("FAIL: client_outdated was accepted")
    except RuntimeError as e:
        ck("message verbatim incl. URL", OUTDATED in str(e), True)
        ck("upgrade URL present", "https://github.com/topcheer/etunnel" in str(e), True)

    if fails:
        print(f"python hello.version tests: FAIL ({fails})")
        return 1
    print("python hello.version tests: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
