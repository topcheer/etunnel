// etunnel SDK for TypeScript (Node >= 22 with built-in WebSocket).
// Same implementation as sdks/node/etunnel.js with types.
// Buffer-based protocol client (request/response bodies buffered per stream;
// WebSocket upgrades are not forwarded — use the Go SDK for those).
//
// import { connect } from './etunnel';
// const t = await connect('wss://t.etun.uk/tunnel', 3000);

import zlib from 'node:zlib';
import { pathToFileURL } from 'node:url';
import { exec } from 'node:child_process';

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

export interface Options {
  /** Multi-target route table: several local services under one public hostname, dispatched by URL path prefix. */
  routes?: Route[];
  token?: string;
  subdomain?: string;
  client?: string;
  /** Server-signed token from a previous session: reclaim the same subdomain. */
  resume?: string;
  onClose?: (reason: string) => void;
  /** Injected WebSocket constructor (the CLI supervisor captures the session socket this way) instead of patching the global, which is unsafe when several connects race on shared event loops. */
  wsFactory?: (url: string) => WebSocket;
}

export interface Tunnel {
  url: string;
  hostname: string;
  subdomain: string;
  /** Server-signed token: pass back as opts.resume to reclaim the subdomain. */
  resumeToken?: string;
  plan: string;
  close(reason?: string): void;
}

interface ReqMsg {
  type: 'req';
  streamId: number;
  method: string;
  path: string;
  headers?: Record<string, string[]>;
}

// payload is a Uint8Array (Buffer extends it): callers pass TextEncoder output
// (Uint8Array) and Buffer slices alike; the empty FIN/CTL frames omit it.
function frame(kind: number, id: number, payload: Uint8Array = new Uint8Array(0)): Buffer {
  const p = payload;
  const b = Buffer.alloc(9 + p.length);
  b.writeUInt8(kind, 0);
  b.writeUInt32BE(id, 1);
  b.writeUInt32BE(p.length, 5);
  b.set(p, 9);
  return b;
}

const HOP = new Set(['connection', 'proxy-connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade']);

// #286 helpers: does `buf` still carry `coding`, or did undici hand us the
// plain entity while the header still declares the coding?
function bodyIsPlain(coding: string, buf: Buffer): boolean {
  try {
    if (coding === 'gzip' || coding === 'x-gzip') return !(buf.length >= 2 && buf[0] === 0x1f && buf[1] === 0x8b);
    if (coding === 'deflate') return !(buf.length >= 1 && buf[0] === 0x78); // zlib wrapper
    if (coding === 'br') { zlib.brotliDecompressSync(buf); return true; }   // decoded OK => plain
    if (coding === 'zstd' && typeof (zlib as any).zstdDecompressSync === 'function') { (zlib as any).zstdDecompressSync(buf); return true; }
  } catch { /* malformed as `coding` => it is still coded */ }
  return false;
}

// Recompress a plain entity back into the coding the local service declared.
// Returns null when we cannot honor the coding - the caller then drops the
// Content-Encoding header instead of shipping a mismatch.
function restoreCoded(coding: string, buf: Buffer): Buffer | null {
  try {
    if (coding === 'gzip' || coding === 'x-gzip') return Buffer.from(zlib.gzipSync(buf));
    if (coding === 'deflate') return Buffer.from(zlib.deflateSync(buf));
    if (coding === 'br') return Buffer.from(zlib.brotliCompressSync(buf));
    if (coding === 'zstd' && typeof (zlib as any).zstdCompressSync === 'function') return Buffer.from((zlib as any).zstdCompressSync(buf));
  } catch { /* fall through: caller drops the declaration */ }
  return null;
}

function pickHostPort(local: string | number): { host: string; port: number } {  if (/^\d+$/.test(String(local))) return { host: '127.0.0.1', port: Number(local) };
  const m = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(local));
  // #243 L7: keep IPv6 brackets; wrap bare IPv6 literals.
  if (m) return { host: m[1], port: Number(m[2]) };
  const h = String(local);
  if (h.includes(':') && !h.startsWith('[')) return { host: `[${h}]`, port: 80 };
  return { host: h, port: 80 };
}


// --- Multi-target local routing (mirrors sdk/route.go semantics) --------
export interface Route { prefix: string; addr: string; strip?: boolean; }
export function normalizeRoutes(routes: Route[]): Route[] {
  const out: Route[] = [];
  const seen = new Set<string>();
  for (let i = 0; i < routes.length; i++) {
    const r = routes[i];
    if (!r || typeof r.prefix !== 'string' || r.prefix === '') {
      throw new Error(`etunnel: route[${i}]: PathPrefix is required (use "/" for the fallback route)`);
    }
    if (!r.addr) throw new Error(`etunnel: route[${i}]: Addr is required`);
    let p = r.prefix;
    if (p[0] !== '/') throw new Error(`etunnel: route[${i}]: PathPrefix "${p}" must start with '/'`);
    while (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1);
    if (seen.has(p)) throw new Error(`etunnel: route[${i}]: duplicate PathPrefix "${p}"`);
    seen.add(p);
    out.push({ prefix: p, addr: r.addr, strip: !!r.strip });
  }
  out.sort((a, b) => b.prefix.length - a.prefix.length);
  return out;
}
export function routeFor(routes: Route[], pathAndQuery: string): { addr: string; path: string } | null {
  const qi = pathAndQuery.indexOf('?');
  const path = qi >= 0 ? pathAndQuery.slice(0, qi) : pathAndQuery;
  const query = qi >= 0 ? pathAndQuery.slice(qi) : '';
  for (const r of routes) {
    if (!(r.prefix === '/' || path === r.prefix || path.startsWith(r.prefix + '/'))) continue;
    let p = path;
    if (r.strip) p = r.prefix === '/' ? path : (path === r.prefix ? '/' : path.slice(r.prefix.length));
    return { addr: r.addr, path: p + query };
  }
  return null;
}

export async function connect(server: string, local: string | number, opts: Options = {}): Promise<Tunnel> {
  // #243 L5: an explicit undefined/null local must not silently target
  // 127.0.0.1:80 - fail like the node and python SDKs.
  if (local === undefined || local === null) throw new Error('etunnel: local address is required');
  const routes = opts.routes ? normalizeRoutes(opts.routes) : null;

  const { host, port } = pickHostPort(local);
  const ws = opts.wsFactory ? opts.wsFactory(server) : new WebSocket(server, 'etunnel-v1');
  // #337-1 (HIGH): handshake-failure paths (proto gate, welcome timeout,
  // error frames) close the socket asynchronously; the close handler and
  // the teardown path touch this state, so it must exist before the ws
  // object does - declaration order used to TDZ-crash the embedder.
  const streams = new Map<number, { req: ReqMsg; body: Buffer[]; size: number }>();
  let closed = false;
  let lastPong = Date.now(); // #243 M3: last pong received from the server
  let lastActivity = Date.now(); // #243 M3: last inbound ws message of any kind

  // #243 M3: single teardown path shared by explicit close() and the ping
  // timeout below, so the pinger never outlives the socket.
  let pinger: ReturnType<typeof setInterval> | null = null;
  // #243 L3: a transport drop (no bye, no ping timeout) must still reach
  // the embedder - closeTunnel is a no-op when already closed.
  ws.onclose = () => { if (!closed) closeTunnel('connection lost'); };
  ws.binaryType = 'arraybuffer';

  await new Promise<void>((res, rej) => {
    ws.onopen = () => res();
    ws.onerror = () => rej(new Error('ws: connect failed'));
  });

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain, token: opts.token, client: opts.client || 'etunnel-ts/1.7.37', resume: opts.resume || undefined }));

  const info = await new Promise<any>((res, rej) => {
    const to = setTimeout(() => {
      // #243 M4: a timed-out handshake must not leave a zombie socket.
      try { (ws as any).close(); } catch { /* gone */ }
      rej(Object.assign(new Error('etunnel: welcome timeout (15s) - server accepted the socket but never sent welcome'), { code: 'sdk_timeout' }));
    }, 15000);
    ws.onmessage = (ev) => {
      // #243 M1: malformed text frames are ignored (Go DecodeControl
      // parity); a bare parse would crash the process.
      let m: any;
      try {
        m = JSON.parse(typeof ev.data === 'string' ? ev.data : Buffer.from(ev.data).toString());
      } catch { return; }
      if (m.type === 'welcome') {
        // #322-6/#330-2: reject newer protocol versions loudly (PROTOCOL.md
        // §3) - decoding v2 traffic as v1 produces silent garbage.
        if (m.proto > 1) {
          clearTimeout(to);
          ws.close();
          rej(Object.assign(new Error(`etunnel: server speaks protocol v${m.proto}; update the SDK`), { code: 'proto_version' }));
          return;
        }
        clearTimeout(to);
        res(m);
      }
      else if (m.type === 'error') { clearTimeout(to); rej(Object.assign(new Error(`etunnel: server rejected: ${m.code}: ${m.message || ''}`), { code: m.code })); }
    };
  });


  function trySend(buf: Buffer): void { try { ws.send(buf); } catch { /* gone */ } }

  function closeTunnel(reason: string): void {
    if (closed) return;
    closed = true;
    if (pinger) clearInterval(pinger);
    try { ws.send(JSON.stringify({ type: 'bye', reason })); } catch { /* gone */ }
    try { ws.close(); } catch { /* gone */ }
    opts.onClose?.(reason);
  }

  ws.onmessage = (ev: MessageEvent) => {
    // #243 M2: 128KiB read limit - control JSON is small and frames cap at
    // 32KiB, so a bigger ws message is a protocol violation (Go tears the
    // socket down via SetReadLimit).
    const size = typeof ev.data === 'string' ? Buffer.byteLength(ev.data) : (ev.data as ArrayBuffer).byteLength;
    if (size > MAX_WS_MSG) { closeTunnel('read limit exceeded'); return; }
    lastActivity = Date.now();
    if (typeof ev.data === 'string') {
      let m: any;
      try { m = JSON.parse(ev.data); } catch { return; } // #243 M1 + #322-2 converged: unknown control types are ignored; malformed KNOWN frames are fatal server-side (PROTOCOL.md §3) - the client stays alive by skipping
      if (m.type === 'req') {
        // #243 M2: refuse a replayed live id or a stream beyond the cap
        // (Go registerClientStream parity) and tell the server with an RST.
        if (streams.has(m.streamId) || streams.size >= MAX_STREAMS) {
          trySend(frame(0x03, m.streamId, Buffer.alloc(0)));
          return;
        }
        streams.set(m.streamId, { req: m, body: [], size: 0 });
      }
      else if (m.type === 'ping') ws.send(JSON.stringify({ type: 'pong', ts: m.ts }));
      else if (m.type === 'pong') lastPong = Date.now(); // #243 M3
      else if (m.type === 'bye') {
        closed = true;
        if (pinger) clearInterval(pinger);
        opts.onClose?.(m.reason);
        try { ws.close(); } catch { /* gone */ }
      }
      return;
    }
    const b = Buffer.from(ev.data as ArrayBuffer);
    const kind = b.readUInt8(0);
    const id = b.readUInt32BE(1);
    const payload = b.subarray(9, 9 + b.readUInt32BE(5));
    const st = streams.get(id);
    if (!st) return;
    if (kind === 0x01) {
      // #243 M2: bound the buffered request body; over the limit the stream
      // is refused with an RST instead of buffering the server into our RAM.
      st.size += payload.length;
      if (st.size > MAX_STREAM_BODY) { streams.delete(id); trySend(frame(0x03, id, Buffer.alloc(0))); return; }
      st.body.push(payload);
    }
    else if (kind === 0x02) { streams.delete(id); handle(st, id); }
    else if (kind === 0x03) streams.delete(id);
  };

  // #243 M3: keepalive loop - ping every 30s; when neither a pong nor any
  // traffic has been seen for 150s the tunnel is dead (NAT blackhole) and
  // the socket is torn down so the supervisor can reconnect.
  pinger = setInterval(() => {
    if (closed) { clearInterval(pinger!); return; }
    try { ws.send(JSON.stringify({ type: 'ping', ts: Math.floor(Date.now() / 1000) })); } catch { return; }
    if (Date.now() - lastPong > PING_DEADLINE && Date.now() - lastActivity > PING_DEADLINE) closeTunnel('ping timeout');
  }, PING_INTERVAL);
  if (typeof (pinger as any)?.unref === 'function') (pinger as any).unref(); // keep the process exitable

  async function handle(st: { req: ReqMsg; body: Buffer[] }, id: number): Promise<void> {
    const body = Buffer.concat(st.body);
    const headers: Record<string, string | string[]> = {};
    for (const [k, vs] of Object.entries(st.req.headers || {})) {
      if (HOP.has(k.toLowerCase())) continue;
      headers[k] = vs; // #243 M6: keep arrays - comma-joining corrupts multi-value headers
    }
    let resp: Response;
    try {
      let dialAddr = `${host}:${port}`;
      let dialPath: string = st.req.path;
      if (routes) {
        const hit = routeFor(routes, dialPath);
        if (!hit) {
          ws.send(JSON.stringify({ type: 'res', streamId: id, status: 404, headers: { 'Content-Type': ['text/plain; charset=utf-8'] } }));
          ws.send(frame(0x01, id, new TextEncoder().encode('404 page not found (no route matches this path)\n')));
          ws.send(frame(0x02, id, new Uint8Array(0)));
          return;
        }
        dialAddr = hit.addr;
        dialPath = hit.path;
      }
      resp = await fetch(`http://${dialAddr}${dialPath}`, {
        method: st.req.method,
        // undici accepts string[] header values; the DOM HeadersInit type
        // does not know that yet (#243 M6).
        headers: headers as unknown as Record<string, string>,
        body: ['GET', 'HEAD'].includes(st.req.method) ? undefined : body,
      });
    } catch (e) {
      ws.send(JSON.stringify({ type: 'res', streamId: id, status: 502, headers: { 'Content-Type': ['text/html; charset=utf-8'] } }));
      ws.send(frame(0x01, id, Buffer.from(localErrorPage((e as Error).message))));
      ws.send(frame(0x02, id));
      return;
    }
    const rh: Record<string, string[]> = {};
    resp.headers.forEach((v, k) => { if (!HOP.has(k.toLowerCase())) (rh[k] = rh[k] || []).push(v); });
    // #286: undici decompresses gzip/deflate/br transparently yet still
    // exposes Content-Encoding on resp.headers. Replaying the decompressed
    // bytes under the declared coding ships a header/entity mismatch (the
    // mirror image of the framing leak: the visitor gunzips identity bytes
    // and fails). Keep the wire honest (PROTOCOL.md §2 identity-body
    // invariant): restore the declared coding when zlib can, otherwise drop
    // the declaration so headers describe the entity that actually travels.
    // `Buffer` (ArrayBufferLike): zlib output re-assigned below is not Buffer<ArrayBuffer>.
    let buf: Buffer = Buffer.from(await resp.arrayBuffer());
    const ceKey = Object.keys(rh).find((k) => k.toLowerCase() === 'content-encoding');
    if (ceKey && buf.length > 0) {
      const coding = (rh[ceKey][rh[ceKey].length - 1] || '').split(',')[0].trim().toLowerCase();
      if (bodyIsPlain(coding, buf)) {
        const restored = restoreCoded(coding, buf);
        if (restored) buf = restored;
        else delete rh[ceKey];
      }
    }
    ws.send(JSON.stringify({ type: 'res', streamId: id, status: resp.status, headers: rh }));
    for (let i = 0; i < buf.length; i += MAXP) ws.send(frame(0x01, id, buf.subarray(i, Math.min(i + MAXP, buf.length))));
    ws.send(frame(0x02, id));
  }

  return {
    url: info.url,
    hostname: info.hostname,
    subdomain: info.subdomain,
    resumeToken: info.resumeToken || '',
    plan: info.plan || '',
    close(reason = 'client closed') { closeTunnel(reason); },
  };
}

// CLI supervisor: keep the tunnel up across drops (ETUNNEL_RECONNECT=0 opts out).
const RETRY_MIN = 500;
const RETRY_MAX = 30000;

// Jittered exponential backoff: 500ms doubling to a 30s ceiling, +/-20% noise
// so a fleet of clients does not stampede after a server restart.
function retryDelay(attempt: number): number {
  const base = Math.min(RETRY_MIN * Math.pow(2, attempt), RETRY_MAX);
  return Math.round(base * (0.8 + Math.random() * 0.4));
}

// connect() does not expose its socket, so capture the WebSocket it constructs
// (the global is only patched for the duration of the call) and watch that.
async function connectWatched(
  server: string,
  local: string | number,
  opts: Options,
): Promise<{ tunnel: Tunnel; ws: WebSocket | null }> {
  // #243 M5: capture the socket through the documented wsFactory hook
  // instead of patching the global WebSocket, which races when several
  // connects share one event loop (node already made this move).
  const box: { ws: WebSocket | null } = { ws: null };
  const watched: Options = {
    ...opts,
    wsFactory: (u: string) => {
      const s = opts.wsFactory ? opts.wsFactory(u) : new WebSocket(u, 'etunnel-v1');
      box.ws = s;
      return s;
    },
  };
  return { tunnel: await connect(server, local, watched), ws: box.ws };
}

// Resolve once the session is over: explicit close(), server bye, or a drop.
function waitClosed(ws: WebSocket | null): Promise<void> {
  return new Promise<void>((res) => {
    if (!ws || ws.readyState === 3) return res();
    ws.addEventListener('close', () => res(), { once: true });
  });
}

async function supervise(server: string, local: string | number, opts: Options): Promise<void> {
  let stopped = false;
  let tunnel: Tunnel | null = null;
  const shutdown = (sig: string): void => {
    if (stopped) return;
    stopped = true; // never reconnect after a signal
    if (tunnel) { try { tunnel.close('received ' + sig); } catch { /* gone */ } }
    process.exit(0);
  };
  process.on('SIGINT', () => shutdown('SIGINT'));
  process.on('SIGTERM', () => shutdown('SIGTERM'));

  let resume = ''; // server-signed token: reclaim the same subdomain on reconnect
  for (let attempt = 0; !stopped; ) {
    try {
      const { tunnel: t, ws } = await connectWatched(server, local, { ...opts, resume });
      tunnel = t;
      console.log('TUNNEL_URL: ' + t.url);
      attempt = 0; // a working session resets the backoff to 500ms
      await waitClosed(ws);
      if (stopped) return;
      console.error('etunnel: tunnel closed, reconnecting');
    } catch (e) {
      if (stopped) return;
      console.error('etunnel:', (e as Error).message);
    }
    const wait = retryDelay(attempt++);
    console.error('etunnel: retrying in ' + wait + 'ms');
    await new Promise<void>((res) => setTimeout(res, wait));
  }
}

// Run directly (node etunnel.ts via type stripping, tsx, or compiled JS) - and
// ONLY then. The previous check, argv[1].endsWith(argv[1].split('/').pop()),
// was a tautology (x always ends with its own last path segment), so merely
// importing this file started the CLI supervisor: a second tunnel plus its
// SIGINT handler on the importing program's process. Comparing the module URL
// with the entry script is the standard ESM "main" test.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  // run directly (node etunnel.ts with type stripping, or compiled js)
  const server = process.env.ETUNNEL_SERVER || 'wss://t.etun.uk/tunnel';
  const local = process.env.ETUNNEL_LOCAL || process.argv[2] || '3000';
  const opts: Options = { token: process.env.ETUNNEL_TOKEN, subdomain: process.env.ETUNNEL_SUBDOMAIN };
  if (process.env.ETUNNEL_RECONNECT === '0') {
    // legacy: connect once, disconnect ends the process
    connect(server, local, opts)
      .then((t) => console.log('TUNNEL_URL: ' + t.url))
      .catch((e: Error) => { console.error('etunnel:', e.message); process.exit(1); });
  } else {
    supervise(server, local, opts).catch((e: Error) => { console.error('etunnel:', e.message); process.exit(1); });
  }
}

// localErrorPage renders the themed 502 shown when the local app is down.
function localErrorPage(msg: string): string {
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
// Persist the result; pass api_token as opts.token on future connect() calls.
// Returns the wire keys as-is for cross-SDK consistency.
export interface Credentials {
  api_token: string;
  subdomains: string[] | null;
  plan: string;
  expires_at: number;
}

export interface LoginOptions {
  /** Override browser opening; receives the auth URL. Default: open/xdg-open, URL printed on failure. */
  openBrowser?: (url: string) => void;
  /** Overall budget, ms. Default 10 minutes (covers a login detour). */
  timeoutMs?: number;
}

function defaultOpenBrowserTS(u: string): void {
  const cmd = process.platform === 'darwin' ? 'open'
    : process.platform === 'win32' ? 'rundll32 url.dll,FileProtocolHandler'
    : 'xdg-open';
  exec(cmd + ' ' + JSON.stringify(u), () => {}); // fire-and-forget; URL also printed
}

export async function login(server: string, opts: LoginOptions = {}): Promise<Credentials> {
  const http = await import('node:http');
  const base = String(server || '').replace(/\/+$/, '');
  if (!base) throw new Error('etunnel: login: server required');
  const timeoutMs = opts.timeoutMs ?? 10 * 60 * 1000;
  let deviceCode = '';

  const code = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => { try { srv.close(); } catch { /* already gone */ } reject(new Error('etunnel: login timed out')); }, timeoutMs);
    timer.unref();
    const srv = http.createServer((req: any, res: any) => {
      const u = new URL(req.url as string, 'http://127.0.0.1');
      const c = u.searchParams.get('code');
      if (u.pathname !== '/callback' || !c) { res.writeHead(404); res.end(); return; }
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end('<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>');
      clearTimeout(timer);
      srv.close();
      resolve(c);
    });
    srv.on('error', (e: Error) => { clearTimeout(timer); reject(e); });
    srv.listen(0, '127.0.0.1', async () => {
      try {
        const redirectUri = 'http://127.0.0.1:' + (srv.address() as { port: number }).port + '/callback';
        const r = await fetch(base + '/auth/cli/start', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ redirect_uri: redirectUri }),
        });
        if (!r.ok) throw new Error('etunnel: login start: HTTP ' + r.status + ' ' + (await r.text()).slice(0, 200));
        const start = await r.json() as { auth_url?: string; device_code?: string };
        if (!start.auth_url || !start.device_code) throw new Error('etunnel: login: empty start payload');
        deviceCode = start.device_code;
        const open = opts.openBrowser || defaultOpenBrowserTS;
        try { open(start.auth_url); } catch { /* headless: the URL is printed below */ }
        process.stderr.write('etunnel: authorize in your browser: ' + start.auth_url + '\n');
      } catch (e) { clearTimeout(timer); try { srv.close(); } catch { /* gone */ } reject(e); }
    });
  });

  const r2 = await fetch(base + '/auth/cli/exchange', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ device_code: deviceCode, code }),
  });
  if (!r2.ok) throw new Error('etunnel: login exchange: HTTP ' + r2.status + ' ' + (await r2.text()).slice(0, 200));
  const creds = await r2.json() as Credentials;
  if (!creds.api_token) throw new Error('etunnel: login: no api_token in exchange response');
  return creds;
}
