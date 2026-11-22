#!/usr/bin/env python3
"""test_proto_gate.py — #322-6 regression: welcome.proto=2 must be rejected.

  python3 tests/test_proto_gate.py    # expect: PASS
"""
import json
import os
import sys
import threading

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import websocket as _ws  # noqa: E402


class FakeConn:
    def __init__(self):
        self._cv = threading.Condition()
        self._msgs = [json.dumps({"type": "welcome", "proto": 2, "url": "https://f.t.test",
                                  "hostname": "f.t.test", "subdomain": "f", "plan": "free"})]
        self.closed = False

    def send(self, _d):
        return None

    def recv(self):
        with self._cv:
            if self._msgs:
                return self._msgs.pop(0)
            raise ConnectionError("closed")

    def settimeout(self, _t):
        return None

    def close(self):
        with self._cv:
            self.closed = True
            self._cv.notify_all()


def main() -> int:
    fake = FakeConn()
    _ws.create_connection = lambda *a, **k: fake
    import etunnel  # noqa: E402

    try:
        etunnel.connect("wss://fake/tunnel", 3000)
        print("FAIL: proto=2 welcome was accepted")
        return 1
    except RuntimeError as e:
        if "protocol v2" in str(e) and fake.closed:
            print(f"PASS: welcome.proto=2 rejected and socket closed: {e}")
            return 0
        print(f"FAIL: wrong rejection: {e}")
        return 1
    except Exception as e:  # noqa: BLE001
        print(f"FAIL: unexpected error type: {type(e).__name__}: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
