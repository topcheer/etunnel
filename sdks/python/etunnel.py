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

# Themed 502 page shown when the local app is down (same HTML as the other SDKs).
_ERR_PAGE_A = """<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - local app down</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#f85149}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#58a6ff;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class="badge">502 local app down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class="panel">
<div class="k">error</div><div class="v err">"""

_ERR_PAGE_B = """</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>"""


def _local_error_page(msg: object) -> str:
    esc = str(msg).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return _ERR_PAGE_A + esc + _ERR_PAGE_B


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
        **({"resume": opts["resume"]} if opts.get("resume") else {}),
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
                                    "headers": {"Content-Type": ["text/html; charset=utf-8"]}}))
                ws.send_binary(_frame(KIND_DATA, sid, _local_error_page(e).encode()))
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
        "resumeToken": info.get("resumeToken", ""),
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
    resume = ""  # server-signed token: reclaim the same subdomain on reconnect
    while not stopped.is_set():
        lost = threading.Event()
        try:
            t = connect(server, local, dict(opts, resume=resume, on_close=lambda _reason: lost.set()))
        except Exception as e:  # noqa: BLE001
            print(f"etunnel: {e}", file=sys.stderr, flush=True)
            if not reconnect:
                sys.exit(1)
        else:
            resume = t.get("resumeToken") or resume
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
