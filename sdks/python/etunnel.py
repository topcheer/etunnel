"""etunnel SDK for Python (uses the `websocket-client` package).

Buffer-based protocol client: request/response bodies are buffered per stream
(WebSocket upgrades are not forwarded — use the Go SDK for those).

    from etunnel import connect
    t = connect("wss://t.etun.uk/tunnel", 3000)
    print(t["url"])
"""

from __future__ import annotations

import json
import os
import random
import signal
import sys
import threading
import time
import urllib.request
import urllib.error
from typing import Any, Optional

import websocket  # pip install websocket-client

MAXP = 32768

HOP = {
    "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
    "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
}

KIND_DATA, KIND_FIN, KIND_RST = 0x01, 0x02, 0x03


def _frame(kind: int, sid: int, payload: bytes = b"") -> bytes:
    return bytes([kind]) + sid.to_bytes(4, "big") + len(payload).to_bytes(4, "big") + payload


def _parse_frame(buf: bytes):
    kind = buf[0]
    sid = int.from_bytes(buf[1:5], "big")
    n = int.from_bytes(buf[5:9], "big")
    return kind, sid, buf[9:9 + n]


def connect(server: str, local, opts: Optional[dict] = None) -> dict:
    """Connect to an etunnel server exposing the local service.

    local: port number or "host:port".
    opts: {token, subdomain, client, on_close}.
    Returns a dict {url, hostname, subdomain, plan, close()}.
    """
    opts = opts or {}
    if isinstance(local, int):
        local = f"127.0.0.1:{local}"
    elif ":" not in str(local):
        local = f"127.0.0.1:{local}"

    ws = websocket.create_connection(server, subprotocols=["etunnel-v1"], timeout=15)
    ws.send(json.dumps({
        "type": "hello", "proto": 1,
        "subdomain": opts.get("subdomain"),
        "token": opts.get("token"),
        "client": opts.get("client", "etunnel-python/1.0"),
    }))

    while True:
        m = json.loads(ws.recv())
        if m.get("type") == "welcome":
            info = m
            break
        if m.get("type") == "error":
            ws.close()
            raise RuntimeError(f"etunnel: {m.get('code')}: {m.get('message', '')}")

    state = {"closed": False}
    streams: dict[int, dict] = {}
    lock = threading.Lock()

    def handle(req: dict, body: bytes, sid: int) -> None:
        headers = {k: ", ".join(vs) for k, vs in (req.get("headers") or {}).items()
                   if k.lower() not in HOP}
        method = req.get("method", "GET")
        data = body if method not in ("GET", "HEAD") else None
        r = urllib.request.Request(f"http://{local}{req.get('path', '/')}", data=data,
                                   headers=headers, method=method)
        try:
            with urllib.request.urlopen(r, timeout=60) as resp:
                status = resp.status
                rh = {k: [v] for k, v in resp.getheaders() if k.lower() not in HOP}
                payload = resp.read()
        except urllib.error.HTTPError as e:
            status = e.code
            rh = {k: [v] for k, v in e.headers.items() if k.lower() not in HOP}
            payload = e.read()
        except Exception as e:  # noqa: BLE001
            with lock:
                ws.send(json.dumps({"type": "res", "streamId": sid, "status": 502,
                                    "headers": {"Content-Type": ["text/plain"]}}))
                ws.send_binary(_frame(KIND_DATA, sid, f"etunnel: local service error: {e}\n".encode()))
                ws.send_binary(_frame(KIND_FIN, sid))
            return
        with lock:
            ws.send(json.dumps({"type": "res", "streamId": sid, "status": status, "headers": rh}))
            for i in range(0, len(payload), MAXP):
                ws.send_binary(_frame(KIND_DATA, sid, payload[i:i + MAXP]))
            ws.send_binary(_frame(KIND_FIN, sid))

    def reader() -> None:
        try:
            while True:
                m = ws.recv()
                if isinstance(m, str):
                    msg = json.loads(m)
                    t = msg.get("type")
                    if t == "req":
                        streams[msg["streamId"]] = {"req": msg, "body": []}
                    elif t == "ping":
                        with lock:
                            ws.send(json.dumps({"type": "pong", "ts": msg.get("ts", 0)}))
                    elif t == "bye":
                        state["closed"] = True
                        if opts.get("on_close"):
                            opts["on_close"](msg.get("reason", ""))
                        ws.close()
                        return
                else:
                    kind, sid, payload = _parse_frame(m)
                    st = streams.get(sid)
                    if not st:
                        continue
                    if kind == KIND_DATA:
                        st["body"].append(payload)
                    elif kind == KIND_FIN:
                        streams.pop(sid, None)
                        body = b"".join(st["body"])
                        threading.Thread(target=handle, args=(st["req"], body, sid), daemon=True).start()
                    elif kind == KIND_RST:
                        streams.pop(sid, None)
        except Exception:
            state["closed"] = True
            if opts.get("on_close"):
                opts["on_close"]("read closed")

    threading.Thread(target=reader, daemon=True).start()

    def close(reason: str = "client closed") -> None:
        if state["closed"]:
            return
        state["closed"] = True
        try:
            ws.send(json.dumps({"type": "bye", "reason": reason}))
        except Exception:
            pass
        ws.close()
        if opts.get("on_close"):
            opts["on_close"](reason)

    return {
        "url": info["url"],
        "hostname": info["hostname"],
        "subdomain": info["subdomain"],
        "plan": info.get("plan", ""),
        "close": close,
    }


def _main() -> None:
    server = os.environ.get("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    local = os.environ.get("ETUNNEL_LOCAL") or (sys.argv[1] if len(sys.argv) > 1 else "3000")
    opts = {
        "token": os.environ.get("ETUNNEL_TOKEN"),
        "subdomain": os.environ.get("ETUNNEL_SUBDOMAIN"),
    }
    reconnect = os.environ.get("ETUNNEL_RECONNECT", "1") != "0"
    stopped = threading.Event()

    def on_signal(*_args: Any) -> None:
        stopped.set()

    def backoff(attempt: int) -> float:
        # 500ms doubling, capped at 30s, +-20% jitter.
        return min(0.5 * (2 ** attempt), 30.0) * (0.8 + random.random() * 0.4)

    def nap(seconds: float) -> None:
        deadline = time.monotonic() + seconds
        while not stopped.is_set() and time.monotonic() < deadline:
            stopped.wait(0.25)

    signal.signal(signal.SIGINT, on_signal)
    signal.signal(signal.SIGTERM, on_signal)

    attempt = 0
    while not stopped.is_set():
        lost = threading.Event()
        try:
            t = connect(server, local, dict(opts, on_close=lambda _reason: lost.set()))
        except Exception as e:  # noqa: BLE001
            print(f"etunnel: {e}", file=sys.stderr, flush=True)
            if not reconnect:
                sys.exit(1)
        else:
            print("TUNNEL_URL:", t["url"], flush=True)
            attempt = 0
            if not reconnect:
                while not stopped.is_set():
                    stopped.wait(0.5)
                try:
                    t["close"]()
                except Exception:  # noqa: BLE001
                    pass
                return
            while not stopped.is_set() and not lost.wait(0.25):
                pass
            if stopped.is_set():
                try:
                    t["close"]()
                except Exception:  # noqa: BLE001
                    pass
                return
            print("etunnel: connection lost", file=sys.stderr, flush=True)
        delay = backoff(attempt)
        attempt += 1
        if stopped.is_set():
            break
        print(f"etunnel: reconnecting in {delay:.1f}s", file=sys.stderr, flush=True)
        nap(delay)


if __name__ == "__main__":
    _main()
