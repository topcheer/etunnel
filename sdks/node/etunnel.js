#!/usr/bin/env node
'use strict';
// etunnel SDK for Node.js (>= 22, built-in WebSocket, zero dependencies).
// Buffer-based protocol client: request/response bodies are buffered per
// stream (fine for typical webhooks and demos; WebSocket upgrades are not
// forwarded — use the Go SDK for full streaming/upgrades).
//
// const { connect } = require('./etunnel');
// const t = await connect('wss://t.etun.uk/tunnel', 3000);
// console.log(t.url);

const MAXP = 32768;

function frame(kind, id, payload) {
  const p = payload || Buffer.alloc(0);
  const b = Buffer.alloc(9 + p.length);
  b.writeUInt8(kind, 0);
  b.writeUInt32BE(id, 1);
  b.writeUInt32BE(p.length, 5);
  p.copy(b, 9);
  return b;
}

const HOP = new Set(['connection', 'proxy-connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade']);

function pickHostPort(local) {
  if (/^\d+$/.test(String(local))) return { host: '127.0.0.1', port: Number(local) };
  const m = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(local));
  if (m) return { host: m[1].replace(/^[\[\]]|$/g, ''), port: Number(m[2]) };
  return { host: String(local), port: 80 };
}

async function connect(server, local, opts = {}) {
  const { host, port } = pickHostPort(local);
  // The WebSocket constructor comes from opts.wsFactory when injected (the CLI
  // supervisor captures the session socket that way) instead of patching the
  // global, which is unsafe when several connects race on shared event loops.
  const ws = opts.wsFactory ? opts.wsFactory(server) : new WebSocket(server, 'etunnel-v1');
  ws.binaryType = 'arraybuffer';

  await new Promise((res, rej) => {
    ws.onopen = res;
    const diag = (e) => {
      const err = e && (e.error || e);
      const parts = [`ws: cannot connect to ${server}`];
      if (err && (err.code || err.errno)) parts.push(`(${err.code || err.errno})`);
      if (err && err.message) parts.push(err.message);
      try { const u = new URL(server); parts.push(`\n  hints: DNS/proxy/firewall blocking ${u.host}? Try: curl -sI ${u.protocol}//${u.host}/`); } catch {}
      rej(new Error(parts.join(' ')));
    };
    ws.onerror = diag;
    ws.onclose = (ev) => { if (ws.readyState !== 1) diag({ error: { message: `connection closed (code ${ev?.code ?? '?'})` } }); };
  });

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain || undefined, token: opts.token || undefined, client: opts.client || 'etunnel-node/1.4.0', resume: opts.resume || undefined }));

  const info = await new Promise((res, rej) => {
    const to = setTimeout(() => rej(new Error('welcome timeout')), 15000);
    ws.onmessage = (ev) => {
      const m = JSON.parse(typeof ev.data === 'string' ? ev.data : Buffer.from(ev.data).toString());
      if (m.type === 'welcome') { clearTimeout(to); res(m); }
      else if (m.type === 'error') { clearTimeout(to); rej(Object.assign(new Error(m.code + ': ' + (m.message || '')), { code: m.code })); }
    };
  });

  const state = { url: info.url, closed: false, streams: new Map() };
  const send = (o) => ws.send(typeof o === 'string' ? o : o);
  const sendBin = (buf) => ws.send(buf);

  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') {
      const m = JSON.parse(ev.data);
      if (m.type === 'req') {
        state.streams.set(m.streamId, { req: m, body: [] });
      } else if (m.type === 'ping') {
        send(JSON.stringify({ type: 'pong', ts: m.ts }));
      } else if (m.type === 'bye') {
        state.closed = true;
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
    if (kind === 0x01) st.body.push(payload);
    else if (kind === 0x02) { state.streams.delete(id); handle(st, id).catch(() => trySend(frame(0x03, id))); }
    else if (kind === 0x03) state.streams.delete(id);
  };

  async function handle(st, id) {
    const body = Buffer.concat(st.body);
    const headers = {};
    let isHop = false;
    for (const [k, vs] of Object.entries(st.req.headers || {})) {
      const lk = k.toLowerCase();
      if (HOP.has(lk)) { if (lk === 'connection' && /upgrade/i.test(String(vs))) isHop = true; continue; }
      headers[k] = vs.join(', ');
    }
    let resp;
    try {
      resp = await fetch(`http://${host}:${port}${st.req.path}`, {
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
    send(JSON.stringify({ type: 'res', streamId: id, status: resp.status, headers: rh }));
    const buf = Buffer.from(await resp.arrayBuffer());
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
    close(reason = 'client closed') {
      if (state.closed) return;
      state.closed = true;
      try { send(JSON.stringify({ type: 'bye', reason })); } catch {}
      try { ws.close(); } catch {}
      if (opts.onClose) opts.onClose(reason);
    },
  };
}

module.exports = { connect };

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
