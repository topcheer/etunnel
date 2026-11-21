#!/usr/bin/env python3
"""test_on_close_dedupe.py — #267 regression: on_close must fire exactly once.

The bug: close() fired on_close with the real reason, its ws.close() made the
reader thread's recv() raise, and the reader's except branch fired
on_close("read closed") a second time with a contradicting reason —
supervisors hooking on_close would double-spawn reconnects.

The fix: the reader's except branch returns early when the tunnel is already
closed.

Method: pure unit test — websocket.create_connection is patched with a fake
connection whose recv() raises after close(); on_close invocations are
counted. No server, no network.

  python3 tests/test_on_close_dedupe.py    # expect: PASS (exactly one fire)
"""
import os
import sys
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import websocket as _ws  # noqa: E402  (after path insert)

WELCOME = {"type": "welcome", "url": "https://f.t.test", "hostname": "f.t.test",
           "subdomain": "f", "plan": "free"}


class FakeConn:
    """recv() serves queued messages, then blocks until closed, then raises
    (mirroring a real socket whose read side errors after close())."""

    def __init__(self):
        self._cv = threading.Condition()
        self._msgs = [__import__("json").dumps(WELCOME)]
        self._closed = False

    def send(self, _data):
        return None

    def recv(self):
        with self._cv:
            if self._msgs:
                return self._msgs.pop(0)
            deadline = time.monotonic() + 5.0
            while not self._closed and time.monotonic() < deadline:
                self._cv.wait(0.05)
            raise ConnectionError("read closed")

    def settimeout(self, _t):
        return None

    def close(self):
        with self._cv:
            self._closed = True
            self._cv.notify_all()


def main() -> int:
    fake = FakeConn()
    _ws.create_connection = lambda *a, **k: fake

    import etunnel  # noqa: E402  (after patch so connect() uses the fake)

    fired = []
    tun = etunnel.connect("wss://fake/tunnel", 3000,
                          {"on_close": lambda reason: fired.append(reason)})
    tun["close"]("client closed test")
    time.sleep(0.8)  # give the reader thread time to hit the guard

    if len(fired) == 1 and fired[0] == "client closed test":
        print("PASS: on_close fired exactly once with the real reason")
        return 0
    print(f"FAIL: on_close fired {len(fired)}x: {fired}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
