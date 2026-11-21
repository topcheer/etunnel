#!/usr/bin/env node
'use strict';
const zlib = require('node:zlib');
// etunnel SDK for Node.js (>= 22, built-in WebSocket, zero dependencies).
// Buffer-based protocol client: request/response bodies are buffered per
// stream (fine for typical webhooks and demos; WebSocket upgrades are not
// forwarded — use the Go SDK for full streaming/upgrades).
//
// const { connect } = require('./etunnel');
// const t = await connect('wss://t.etun.uk/tunnel', 3000);
// console.log(t.url);

const MAXP = 32768;

// #243 M2: Go-parity resource limits (sdk/stream.go: maxClientStreams=256,
// SetReadLimit(128KiB)). A hostile or buggy server must not be able to OOM
// the client: refuse streams over the cap with an RST, RST a stream whose
// buffered request body passes MAX_STREAM_BODY, drop the socket on a single
// ws message above the read limit.
const MAX_STREAMS = 256;
const MAX_STREAM_BODY = 16 * 1024 * 1024;
const MAX_WS_MSG = 128 * 1024;
// #243 M3: client-side keepalive (Go pingLoop parity) - ping every 30s and
// drop the tunnel when neither pongs nor traffic arrive for 150s; without it
// a NAT blackhole keeps the client believing the tunnel is alive.
const PING_INTERVAL = 30 * 1000;
const PING_DEADLINE = 150 * 1000;

function frame(kind, id, payload) {
  const p = payload || Buffer.alloc(0);
  const b = Buffer.alloc(9 + p.length);
  b.writeUInt8(kind, 0);
  b.writeUInt32BE(id, 1);
  b.writeUInt32BE(p.length, 5);
  p.copy(b, 9);
  return b;
}

const { normalizeRoutes, routeFor } = require('./route');
const HOP = new Set(['connection', 'proxy-connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade']);

// #286 helpers: does `buf` still carry `coding`, or did undici hand us the
// plain entity while the header still declares the coding?
function bodyIsPlain(coding, buf) {
  try {
    if (coding === 'gzip' || coding === 'x-gzip') return !(buf.length >= 2 && buf[0] === 0x1f && buf[1] === 0x8b);
    if (coding === 'deflate') return !(buf.length >= 1 && buf[0] === 0x78); // zlib wrapper
    if (coding === 'br') { zlib.brotliDecompressSync(buf); return true; }   // decoded OK => plain
    if (coding === 'zstd' && typeof zlib.zstdDecompressSync === 'function') { zlib.zstdDecompressSync(buf); return true; }
  } catch { /* malformed as `coding` => it is still coded */ }
  return false;
}

// Recompress a plain entity back into the coding the local service declared.
// Returns null when we cannot honor the coding - the caller then drops the
// Content-Encoding header instead of shipping a mismatch.
function restoreCoded(coding, buf) {
  try {
    if (coding === 'gzip' || coding === 'x-gzip') return Buffer.from(zlib.gzipSync(buf));
    if (coding === 'deflate') return Buffer.from(zlib.deflateSync(buf));
    if (coding === 'br') return Buffer.from(zlib.brotliCompressSync(buf));
    if (coding === 'zstd' && typeof zlib.zstdCompressSync === 'function') return Buffer.from(zlib.zstdCompressSync(buf));
  } catch { /* fall through: caller drops the declaration */ }
  return null;
}

function pickHostPort(local) {
  if (/^\d+$/.test(String(local))) return { host: '127.0.0.1', port: Number(local) };
  const m = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(local));
  // #243 L7: keep the brackets on IPv6 hosts - stripping them makes
  // `http://${host}:${port}` an invalid URL - and wrap a bare IPv6 literal.
  if (m) return { host: m[1], port: Number(m[2]) };
  const h = String(local);
  if (h.includes(':') && !h.startsWith('[')) return { host: `[${h}]`, port: 80 };
  return { host: h, port: 80 };
}

async function connect(server, local, opts = {}) {
  const routes = opts.routes ? normalizeRoutes(opts.routes) : null;
  if (routes && !routes.length) throw new Error('etunnel: routes must not be empty');
  if (!routes && local === undefined) throw new Error('etunnel: local address is required');
  const single = pickHostPort(local || '127.0.0.1:80');
  const { host, port } = single;
  // The WebSocket constructor comes from opts.wsFactory when injected (the CLI
  // supervisor captures the session socket that way) instead of patching the
  // global, which is unsafe when several connects race on shared event loops.
  const ws = opts.wsFactory ? opts.wsFactory(server) : new WebSocket(server, 'etunnel-v1');
  ws.binaryType = 'arraybuffer';

  await new Promise((res, rej) => {
    ws.onopen = res;
    const diag = (e) => {
      const err = e && (e.error || e);
      const parts = [`etunnel: ws: cannot connect to ${server}`];
      if (err && (err.code || err.errno)) parts.push(`(${err.code || err.errno})`);
      if (err && err.message) parts.push(err.message);
      try { const u = new URL(server); parts.push(`\n  hints: DNS/proxy/firewall blocking ${u.host}? Try: curl -sI ${u.protocol}//${u.host}/`); } catch {}
      rej(Object.assign(new Error(parts.join(' ')), { code: 'sdk_connect' }));
    };
    ws.onerror = diag;
    ws.onclose = (ev) => {
      if (ws.readyState !== 1) diag({ error: { message: `connection closed (code ${ev?.code ?? '?'})` } });
      // #243 L3: a transport drop (no bye, no ping timeout) must still reach
      // the embedder - closeTunnel is a no-op when already closed.
      closeTunnel('connection lost');
    };
  });

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain || undefined, token: opts.token || undefined, client: opts.client || 'etunnel-node/1.6.12', resume: opts.resume || undefined, policy: opts.policy || undefined }));

  const info = await new Promise((res, rej) => {
    const to = setTimeout(() => {
      // #243 M4: a timed-out handshake must not leave a zombie socket
      // behind - close it before rejecting.
      try { ws.close(); } catch {}
      rej(Object.assign(new Error('etunnel: welcome timeout (15s) - server accepted the socket but never sent welcome'), { code: 'sdk_timeout' }));
    }, 15000);
    ws.onmessage = (ev) => {
      // #243 M1: malformed text frames are ignored (Go DecodeControl
      // parity); a bare parse would crash the process.
      let m;
      try {
        m = JSON.parse(typeof ev.data === 'string' ? ev.data : Buffer.from(ev.data).toString());
      } catch { return; }
      if (m.type === 'welcome') { clearTimeout(to); res(m); }
      else if (m.type === 'error') { clearTimeout(to); rej(Object.assign(new Error(`etunnel: server rejected: ${m.code}: ${m.message || ''}`), { code: m.code })); }
    };
  });

  const state = { url: info.url, closed: false, streams: new Map() };
  const send = (o) => ws.send(typeof o === 'string' ? o : o);
  const sendBin = (buf) => ws.send(buf);
  let lastPong = Date.now(); // #243 M3: last pong received from the server
  let lastActivity = Date.now(); // #243 M3: last inbound ws message of any kind

  // #243 M3: single teardown path shared by explicit close() and the ping
  // timeout below, so the pinger never outlives the socket.
  let pinger = null;
  function closeTunnel(reason) {
    if (state.closed) return;
    state.closed = true;
    if (pinger) clearInterval(pinger);
    try { send(JSON.stringify({ type: 'bye', reason })); } catch {}
    try { ws.close(); } catch {}
    if (opts.onClose) opts.onClose(reason);
  }

  ws.onmessage = (ev) => {
    // #243 M2: 128KiB read limit - control JSON is small and frames cap at
    // 32KiB, so a bigger ws message is a protocol violation (Go tears the
    // socket down via SetReadLimit).
    const size = typeof ev.data === 'string' ? Buffer.byteLength(ev.data) : ev.data.byteLength;
    if (size > MAX_WS_MSG) { closeTunnel('read limit exceeded'); return; }
    lastActivity = Date.now();
    if (typeof ev.data === 'string') {
      let m;
      try { m = JSON.parse(ev.data); } catch { return; } // #243 M1: ignore malformed frames
      if (m.type === 'req') {
        // #243 M2: refuse a replayed live id or a stream beyond the cap
        // (Go registerClientStream parity) and tell the server with an RST.
        if (state.streams.has(m.streamId) || state.streams.size >= MAX_STREAMS) {
          trySend(frame(0x03, m.streamId));
          return;
        }
        state.streams.set(m.streamId, { req: m, body: [], size: 0 });
      } else if (m.type === 'ping') {
        send(JSON.stringify({ type: 'pong', ts: m.ts }));
      } else if (m.type === 'pong') {
        lastPong = Date.now(); // #243 M3
      } else if (m.type === 'bye') {
        state.closed = true;
        if (pinger) clearInterval(pinger);
        if (opts.onClose) opts.onClose(m.reason);
        try { ws.close(); } catch {}
      }
      return;
    }
    const b = Buffer.from(ev.data);
    const kind = b.readUInt8(0);
    const id = b.readUInt32BE(1);
    const payload = b.subarray(9, 9 + b.readUInt32BE(5));
    const st = state.streams.get(id);
    if (!st) return;
    if (kind === 0x01) {
      // #243 M2: bound the buffered request body; over the limit the stream
      // is refused with an RST instead of buffering the server into our RAM.
      st.size += payload.length;
      if (st.size > MAX_STREAM_BODY) { state.streams.delete(id); trySend(frame(0x03, id)); return; }
      st.body.push(payload);
    }
    else if (kind === 0x02) { state.streams.delete(id); handle(st, id).catch(() => trySend(frame(0x03, id))); }
    else if (kind === 0x03) state.streams.delete(id);
  };

  // #243 M3: keepalive loop - ping every 30s; when neither a pong nor any
  // traffic has been seen for 150s the tunnel is dead (NAT blackhole) and
  // the socket is torn down so the supervisor can reconnect.
  pinger = setInterval(() => {
    if (state.closed) { clearInterval(pinger); return; }
    try { send(JSON.stringify({ type: 'ping', ts: Math.floor(Date.now() / 1000) })); } catch { return; }
    if (Date.now() - lastPong > PING_DEADLINE && Date.now() - lastActivity > PING_DEADLINE) closeTunnel('ping timeout');
  }, PING_INTERVAL);
  if (pinger.unref) pinger.unref(); // keep the process exitable

  async function handle(st, id) {
    const body = Buffer.concat(st.body);
    const headers = {};
    let isHop = false;
    for (const [k, vs] of Object.entries(st.req.headers || {})) {
      const lk = k.toLowerCase();
      if (HOP.has(lk)) { if (lk === 'connection' && /upgrade/i.test(String(vs))) isHop = true; continue; }
      headers[k] = vs; // #243 M6: keep arrays - joining with commas corrupts multi-value headers
    }
    let target = `http://${host}:${port}`;
    let path = st.req.path;
    if (routes) {
      const hit = routeFor(routes, path);
      if (!hit) {
        send(JSON.stringify({ type: 'res', streamId: id, status: 404, headers: { 'Content-Type': ['text/plain; charset=utf-8'] } }));
        sendBin(frame(0x01, id, Buffer.from('404 page not found (no route matches this path)\n')));
        sendBin(frame(0x02, id, Buffer.alloc(0)));
        return;
      }
      target = `http://${hit.addr}`;
      path = hit.path;
    }
    let resp;
    try {
      resp = await fetch(`${target}${path}`, {
        method: st.req.method,
        headers,
        body: ['GET', 'HEAD'].includes(st.req.method) ? undefined : body,
      });
    } catch (e) {
      send(JSON.stringify({ type: 'res', streamId: id, status: 502, headers: { 'Content-Type': ['text/html; charset=utf-8'] } }));
      sendBin(frame(0x01, id, Buffer.from(localErrorPage(e.message))));
      sendBin(frame(0x02, id));
      return;
    }
    const rh = {};
    resp.headers.forEach((v, k) => { const lk = k.toLowerCase(); if (!HOP.has(lk)) (rh[k] = rh[k] || []).push(v); });
    // #286: undici decompresses gzip/deflate/br transparently yet still
    // exposes Content-Encoding on resp.headers - keep the declared coding
    // and the replayed entity consistent (PROTOCOL.md §2 identity-body
    // invariant); restore the coding, or drop the declaration if we cannot.
    let buf = Buffer.from(await resp.arrayBuffer());
    const ceKey = Object.keys(rh).find((k) => k.toLowerCase() === 'content-encoding');
    if (ceKey && buf.length > 0) {
      const coding = (rh[ceKey][rh[ceKey].length - 1] || '').split(',')[0].trim().toLowerCase();
      if (bodyIsPlain(coding, buf)) {
        const restored = restoreCoded(coding, buf);
        if (restored) buf = restored;
        else delete rh[ceKey];
      }
    }
    send(JSON.stringify({ type: 'res', streamId: id, status: resp.status, headers: rh }));
    for (let i = 0; i < buf.length; i += MAXP) sendBin(frame(0x01, id, buf.subarray(i, Math.min(i + MAXP, buf.length))));
    sendBin(frame(0x02, id));
  }

  function trySend(buf) { try { ws.send(buf); } catch {} }

  return {
    url: info.url,
    hostname: info.hostname,
    subdomain: info.subdomain,
    resumeToken: info.resumeToken || '',
    plan: info.plan || '',
    close(reason = 'client closed') { closeTunnel(reason); },
  };
}

module.exports = { connect, normalizeRoutes, routeFor };

// CLI supervisor: keep the tunnel up across drops (ETUNNEL_RECONNECT=0 opts out).
const RETRY_MIN = 500;
const RETRY_MAX = 30000;

// Jittered exponential backoff: 500ms doubling to a 30s ceiling, +/-20% noise
// so a fleet of clients does not stampede after a server restart.
function retryDelay(attempt) {
  const base = Math.min(RETRY_MIN * Math.pow(2, attempt), RETRY_MAX);
  return Math.round(base * (0.8 + Math.random() * 0.4));
}

// connect() does not expose its socket, so inject a WebSocket factory
// (opts.wsFactory) that captures the constructed socket - no global patching,
// which would be unsafe if several connects ran concurrently.
async function connectWatched(server, local, opts) {
  let ws = null;
  const factory = (url) => { ws = new WebSocket(url, 'etunnel-v1'); return ws; };
  return { tunnel: await connect(server, local, { ...opts, wsFactory: factory }), ws };
}

// Resolve once the session is over: explicit close(), server bye, or a drop.
function waitClosed(ws) {
  return new Promise((res) => {
    if (!ws || ws.readyState === 3) return res();
    ws.addEventListener('close', () => res(), { once: true });
  });
}

async function supervise(server, local, opts) {
  let stopped = false;
  let tunnel = null;
  const shutdown = (sig) => {
    if (stopped) return;
    stopped = true; // never reconnect after a signal
    if (tunnel) { try { tunnel.close('received ' + sig); } catch {} }
    process.exit(0);
  };
  process.on('SIGINT', () => shutdown('SIGINT'));
  process.on('SIGTERM', () => shutdown('SIGTERM'));

  let resume = ''; // server-signed token: reclaim the same subdomain on reconnect
  for (let attempt = 0; !stopped; ) {
    try {
      const { tunnel: t, ws } = await connectWatched(server, local, { ...opts, resume });
      tunnel = t;
      resume = t.resumeToken || resume;
      console.log('TUNNEL_URL: ' + t.url);
      attempt = 0; // a working session resets the backoff to 500ms
      await waitClosed(ws);
      if (stopped) return;
      console.error('etunnel: tunnel closed, reconnecting');
    } catch (e) {
      if (stopped) return;
      console.error('etunnel:', e.message);
    }
    const wait = retryDelay(attempt++);
    console.error('etunnel: retrying in ' + wait + 'ms');
    await new Promise((res) => setTimeout(res, wait));
  }
}

if (require.main === module) {
  const server = process.env.ETUNNEL_SERVER || 'wss://t.etun.uk/tunnel';
  const local = process.env.ETUNNEL_LOCAL || process.argv[2] || '3000';
  const opts = { token: process.env.ETUNNEL_TOKEN, subdomain: process.env.ETUNNEL_SUBDOMAIN };
  if (process.env.ETUNNEL_RECONNECT === '0') {
    // legacy: connect once, disconnect ends the process
    connect(server, local, opts)
      .then((t) => { console.log('TUNNEL_URL: ' + t.url); })
      .catch((e) => { console.error('etunnel:', e.message); process.exit(1); });
  } else {
    supervise(server, local, opts).catch((e) => { console.error('etunnel:', e.message); process.exit(1); });
  }
}


// localErrorPage renders the themed 502 shown when the local app is down.
function localErrorPage(msg) {
  const esc = String(msg).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  return `<!doctype html><html><head><meta charset="utf-8">
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
<div class="k">error</div><div class="v err">${esc}</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>`;
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
// await login('https://t.etun.uk') -> { api_token, subdomains, plan, expires_at }
// Binds a random 127.0.0.1 port, asks the server for an auth URL, opens the
// browser (prints the URL when headless / opener missing), waits for the
// one-time code on /callback (10-min budget), exchanges it. Persist the
// result and pass api_token as opts.token on future connect() calls.
// Returns the wire keys as-is for cross-SDK consistency.
function defaultOpenBrowser(u) {
  const { exec } = require('child_process');
  const cmd = process.platform === 'darwin' ? 'open'
    : process.platform === 'win32' ? 'rundll32 url.dll,FileProtocolHandler'
    : 'xdg-open';
  exec(cmd + ' ' + JSON.stringify(u), () => {}); // fire-and-forget; URL also printed
}

async function login(server, opts = {}) {
  const http = require('http');
  const base = String(server || '').replace(/\/+$/, '');
  if (!base) throw new Error('etunnel: login: server required');
  const timeoutMs = opts.timeoutMs || 10 * 60 * 1000;
  let deviceCode = '';

  const code = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { try { srv.close(); } catch {} reject(new Error('etunnel: login timed out')); }, timeoutMs);
    timer.unref();
    const srv = http.createServer((req, res) => {
      const u = new URL(req.url, 'http://127.0.0.1');
      const c = u.searchParams.get('code');
      if (u.pathname !== '/callback' || !c) { res.writeHead(404); res.end(); return; }
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end('<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>');
      clearTimeout(timer);
      srv.close();
      resolve(c);
    });
    srv.on('error', (e) => { clearTimeout(timer); reject(e); });
    srv.listen(0, '127.0.0.1', async () => {
      try {
        const redirectUri = 'http://127.0.0.1:' + srv.address().port + '/callback';
        const r = await fetch(base + '/auth/cli/start', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ redirect_uri: redirectUri }),
        });
        if (!r.ok) throw new Error('etunnel: login start: HTTP ' + r.status + ' ' + (await r.text()).slice(0, 200));
        const start = await r.json();
        if (!start.auth_url || !start.device_code) throw new Error('etunnel: login: empty start payload');
        deviceCode = start.device_code;
        const open = opts.openBrowser || defaultOpenBrowser;
        try { open(start.auth_url); } catch { /* headless: the URL is printed below */ }
        process.stderr.write('etunnel: authorize in your browser: ' + start.auth_url + '\n');
      } catch (e) { clearTimeout(timer); try { srv.close(); } catch {} reject(e); }
    });
  });

  const r2 = await fetch(base + '/auth/cli/exchange', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ device_code: deviceCode, code }),
  });
  if (!r2.ok) throw new Error('etunnel: login exchange: HTTP ' + r2.status + ' ' + (await r2.text()).slice(0, 200));
  const creds = await r2.json();
  if (!creds.api_token) throw new Error('etunnel: login: no api_token in exchange response');
  return creds;
}

module.exports = Object.assign(module.exports, { login });
