// etunnel SDK for HarmonyOS (ArkTS, @ohos.net.websocket).
// Single-file port of sdks/typescript/etunnel.ts to the ArkTS strict subset:
// no `any`, no optional-call syntax, no object spread, explicit interfaces,
// Uint8Array/DataView framing instead of Buffer.
// Buffer-based protocol client (request/response bodies buffered per stream;
// WebSocket upgrades are not forwarded - use the Go SDK for those).
//
// DevEco Studio (ohpm, from source - the package is NOT on the ohpm registry
// yet, see README "Publishing status"):
//   ohpm install ./path/to/etunnel-harmonyos
//   import { connect } from 'etunnel';
//   const t = await connect('wss://t.etun.uk/tunnel', 3000);
//   console.log('TUNNEL_URL: ' + t.url);
// Local HTTP replays go through @ohos.net.http (no fetch in ArkTS).
import { webSocket } from '@ohos.net.websocket';
import { http } from '@ohos.net.http';

const MAXP = 32768;

// #243 M2 Go-parity resource limits (sdk/stream.go: maxClientStreams=256,
// SetReadLimit(128KiB)): refuse streams over the cap with an RST, RST a
// stream whose buffered request body passes MAX_STREAM_BODY, drop the
// socket on a single ws message above the read limit.
const MAX_STREAMS = 256;
const MAX_STREAM_BODY = 16 * 1024 * 1024;
const MAX_WS_MSG = 128 * 1024;
// #243 M3 client-side keepalive (Go pingLoop parity): ping every 30s and
// drop the tunnel when neither pongs nor traffic arrive for 150s; without
// it a NAT blackhole keeps the client believing the tunnel is alive.
const PING_INTERVAL = 30 * 1000;
const PING_DEADLINE = 150 * 1000;

/** SDK version reported in `hello.version`. Stamped from the repository
 * VERSION by scripts/bump-release.py (MANIFESTS entry below). */
export const VERSION = '1.7.44';

export interface Route {
  prefix: string;
  addr: string;
  strip?: boolean;
}

export interface Options {
  /** Multi-target route table: several local services under one public hostname. */
  routes?: Route[];
  token?: string;
  subdomain?: string;
  client?: string;
  /** Server-signed token from a previous session: reclaim the same subdomain. */
  resume?: string;
  onClose?: (reason: string) => void;
  /** Override the 15s welcome timeout (diagnostic/test hook). */
  welcomeTimeoutMs?: number;
}

export interface Tunnel {
  url: string;
  hostname: string;
  subdomain: string;
  /** Server-signed token: pass back as opts.resume to reclaim the subdomain. */
  resumeToken: string;
  plan: string;
  close(reason?: string): void;
}

export interface EtunnelConfig {
  server: string;
  /** "port" or "host:port" (ETUNNEL_LOCAL parity, host:port supported). */
  local: string;
  token?: string;
  subdomain?: string;
  reconnect: boolean;
}

// --- wire message shapes (protocol/message.go) --------------------------
interface WelcomeMsg {
  type: string;
  url: string;
  hostname: string;
  subdomain: string;
  resumeToken?: string;
  plan?: string;
}

interface ReqMsg {
  type: string;
  streamId: number;
  method: string;
  path: string;
  headers?: Record<string, string[]>;
}

interface HelloMsg {
  type: string;
  proto: number;
  version?: string;
  subdomain?: string;
  token?: string;
  resume?: string;
  client: string;
}

interface PongMsg {
  type: string;
  ts?: number;
}

interface ByeMsg {
  type: string;
  reason?: string;
}

interface StreamState {
  req: ReqMsg;
  body: Uint8Array[];
  size: number;
}

const HOP: string[] = ['connection', 'proxy-connection', 'keep-alive',
  'proxy-authenticate', 'proxy-authorization', 'te', 'trailer',
  'transfer-encoding', 'upgrade'];

function isHop(name: string): boolean {
  return HOP.indexOf(name.toLowerCase()) >= 0;
}

// --- framing (protocol/frame.go: kind | uint32 streamId | uint32 len) ---
function frame(kind: number, id: number, payload: Uint8Array): ArrayBuffer {
  const buf = new ArrayBuffer(9 + payload.byteLength);
  const dv = new DataView(buf);
  dv.setUint8(0, kind);
  dv.setUint32(1, id, false);
  dv.setUint32(5, payload.byteLength, false);
  new Uint8Array(buf, 9).set(payload);
  return buf;
}

function ctlFrame(kind: number, id: number): ArrayBuffer {
  return frame(kind, id, new Uint8Array(0));
}

function parseFrame(ab: ArrayBuffer): { kind: number; id: number; len: number } {
  const dv = new DataView(ab, 0, Math.min(9, ab.byteLength));
  return { kind: dv.getUint8(0), id: dv.getUint32(1, false), len: dv.getUint32(5, false) };
}

// --- text <-> utf8 (no Buffer/util dependency; handles surrogate pairs) -
export function utf8Encode(s: string): Uint8Array {
  const out: number[] = [];
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c >= 0xD800 && c <= 0xDBFF && i + 1 < s.length) {
      const c2 = s.charCodeAt(i + 1);
      if (c2 >= 0xDC00 && c2 <= 0xDFFF) {
        c = 0x10000 + ((c - 0xD800) << 10) + (c2 - 0xDC00);
        i++;
      }
    }
    if (c < 0x80) {
      out.push(c);
    } else if (c < 0x800) {
      out.push(0xC0 | (c >> 6), 0x80 | (c & 63));
    } else if (c < 0x10000) {
      out.push(0xE0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
    } else {
      out.push(0xF0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
    }
  }
  return new Uint8Array(out);
}

export function utf8Decode(bytes: Uint8Array): string {
  let s = '';
  let i = 0;
  while (i < bytes.length) {
    const b = bytes[i];
    let cp: number;
    if (b < 0x80) {
      cp = b;
      i += 1;
    } else if (b < 0xE0 && i + 1 < bytes.length) {
      cp = ((b & 0x1F) << 6) | (bytes[i + 1] & 63);
      i += 2;
    } else if (b < 0xF0 && i + 2 < bytes.length) {
      cp = ((b & 0x0F) << 12) | ((bytes[i + 1] & 63) << 6) | (bytes[i + 2] & 63);
      i += 3;
    } else if (i + 3 < bytes.length) {
      cp = ((b & 0x07) << 18) | ((bytes[i + 1] & 63) << 12) | ((bytes[i + 2] & 63) << 6) | (bytes[i + 3] & 63);
      i += 4;
    } else {
      cp = b; // truncated tail: emit the byte, stop
      i += 1;
    }
    if (cp > 0xFFFF) {
      cp -= 0x10000;
      s += String.fromCharCode(0xD800 + (cp >> 10), 0xDC00 + (cp & 0x3FF));
    } else {
      s += String.fromCharCode(cp);
    }
  }
  return s;
}

/** #283-2: HTTP method passthrough - unknown verbs (PROPFIND, REPORT,
 *  TRACE, custom) must reach the local service verbatim, not be silently
 *  rewritten to GET (TS fetch and the Android SDK both pass through). */
export function httpMethodFor(method: string): string {
  return method;
}

// --- ETUNNEL_* contract (AGENTS.md parity) ------------------------------
// HarmonyOS apps have no process environment: the standard variables are
// resolved from a plain string map the embedder provides (preferences,
// BuildConfig-equivalents, a debug launcher). Same keys and defaults as
// every other SDK.
export function configFromEnv(env: Record<string, string>): EtunnelConfig {
  const local = env['ETUNNEL_LOCAL'];
  const server = env['ETUNNEL_SERVER'];
  const token = env['ETUNNEL_TOKEN'];
  const subdomain = env['ETUNNEL_SUBDOMAIN'];
  const reconnectRaw = env['ETUNNEL_RECONNECT'];
  return {
    server: server && server.length > 0 ? server : 'wss://t.etun.uk/tunnel',
    local: local && local.length > 0 ? local : '3000',
    token: token && token.length > 0 ? token : undefined,
    subdomain: subdomain && subdomain.length > 0 ? subdomain : undefined,
    // "0" opts out of the reconnect loop; anything else reconnects (parity).
    reconnect: reconnectRaw !== '0',
  };
}

function pickHostPort(local: string): { host: string; port: number } {
  if (/^\d+$/.test(local)) {
    return { host: '127.0.0.1', port: Number(local) };
  }
  const m = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(local);
  // #243 L7: keep IPv6 brackets; wrap bare IPv6 literals.
  if (m !== null) {
    return { host: m[1], port: Number(m[2]) };
  }
  if (local.indexOf(':') >= 0 && local[0] !== '[') {
    return { host: '[' + local + ']', port: 80 };
  }
  return { host: local, port: 80 };
}

// --- multi-target local routing (mirrors sdk/route.go semantics) --------
export function normalizeRoutes(routes: Route[]): Route[] {
  const out: Route[] = [];
  const seen: Set<string> = new Set<string>();
  for (let i = 0; i < routes.length; i++) {
    const r = routes[i];
    if (!r || typeof r.prefix !== 'string' || r.prefix === '') {
      throw new Error('etunnel: route[' + i + ']: PathPrefix is required (use "/" for the fallback route)');
    }
    if (!r.addr) {
      throw new Error('etunnel: route[' + i + ']: Addr is required');
    }
    let p = r.prefix;
    if (p[0] !== '/') {
      throw new Error('etunnel: route[' + i + ']: PathPrefix "' + p + '" must start with \'/\'');
    }
    while (p.length > 1 && p[p.length - 1] === '/') {
      p = p.slice(0, -1);
    }
    if (seen.has(p)) {
      throw new Error('etunnel: route[' + i + ']: duplicate PathPrefix "' + p + '"');
    }
    seen.add(p);
    out.push({ prefix: p, addr: r.addr, strip: r.strip === true });
  }
  out.sort((a, b) => b.prefix.length - a.prefix.length);
  return out;
}

export function routeFor(routes: Route[], pathAndQuery: string): { addr: string; path: string } | null {
  const qi = pathAndQuery.indexOf('?');
  const path = qi >= 0 ? pathAndQuery.slice(0, qi) : pathAndQuery;
  const query = qi >= 0 ? pathAndQuery.slice(qi) : '';
  for (const r of routes) {
    if (!(r.prefix === '/' || path === r.prefix || path.startsWith(r.prefix + '/'))) {
      continue;
    }
    let p = path;
    if (r.strip === true) {
      p = r.prefix === '/' ? path : (path === r.prefix ? '/' : path.slice(r.prefix.length));
    }
    return { addr: r.addr, path: p + query };
  }
  return null;
}

// localErrorPage renders the themed 502 shown when the local app is down.
function localErrorPage(msg: string): string {
  const esc = String(msg).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  return '<!doctype html><html><head><meta charset="utf-8">' +
    '<meta name="viewport" content="width=device-width, initial-scale=1">' +
    '<title>502 - local app down</title>' +
    '<style>:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}' +
    '*{box-sizing:border-box;margin:0;padding:0}' +
    'body{font-family:ui-monospace,Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);' +
    'display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}' +
    'main{max-width:600px;padding:2.5rem 1.5rem;width:100%}' +
    '.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;' +
    'padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}' +
    'h1{font-size:1.6rem;margin-bottom:.4rem}h1 .g{color:var(--green)}' +
    'p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}' +
    '.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}' +
    '.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;margin-bottom:.3rem}' +
    '.panel .v{font-size:.9rem;word-break:break-all}.err{color:#f85149}' +
    '.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}' +
    '.tip b{color:var(--fg)}footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}' +
    'footer a{color:#58a6ff;text-decoration:none}' +
    '</style></head><body><main>' +
    '<span class="badge">502 local app down</span>' +
    '<h1>tunnel up, <span class="g">local app</span> is not</h1>' +
    '<p>The tunnel connection is alive, but nothing answered when we knocked.</p>' +
    '<div class="panel"><div class="k">error</div><div class="v err">' + esc + '</div></div>' +
    '<div class="tip"><b>Fix:</b> start your local service and reload - ' +
    'the tunnel stays connected and keeps retrying for you.</div>' +
    '<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>' +
    '</main></body></html>';
}

/** Open the tunnel: one outbound WebSocket (subprotocol etunnel-v1), hello,
 *  welcome, then HTTP replays to the local service. Resolves only after the
 *  server's welcome; throws on error/welcome-timeout (15s). */
export async function connect(server: string, local: string, opts: Options = {}): Promise<Tunnel> {
  if (local === undefined || local === null || local.length === 0) {
    // #243 L5: an empty local must not silently target 127.0.0.1:80.
    throw new Error('etunnel: local address is required');
  }
  const routes = opts.routes ? normalizeRoutes(opts.routes) : null;
  const hp = pickHostPort(local);

  const ws = webSocket.createWebSocket();
  let closed = false;
  let handshakeDone = false;
  let welcomeResolve: ((m: WelcomeMsg) => void) | null = null;
  let welcomeReject: ((e: Error) => void) | null = null;
  let openResolve: (() => void) | null = null;
  let openReject: ((e: Error) => void) | null = null;
  const streams: Map<number, StreamState> = new Map<number, StreamState>();
  let lastPong = Date.now(); // #243 M3
  let lastActivity = Date.now(); // #243 M3
  let pinger = 0;
  const handshakeTimer = setTimeout(() => {
    if (!handshakeDone) {
      // #283-1: a timed-out handshake must not leave a zombie socket (TS
      // #243 M4 parity) - drop every listener and close, or supervise()
      // re-dials accumulate fds and server session slots without bound.
      const types: string[] = ['open', 'message', 'close', 'error'];
      for (const t of types) {
        ws.off(t);
      }
      try {
        ws.close(1000, 'welcome timeout');
      } catch (e) { /* gone */ }
      const e = new Error('etunnel: welcome timeout - server accepted the socket but never sent welcome');
      if (openReject) openReject(e);
      if (welcomeReject) welcomeReject(e);
    }
  }, opts.welcomeTimeoutMs ? opts.welcomeTimeoutMs : 15000);

  function trySend(data: string | ArrayBuffer): void {
    try {
      ws.send(data);
    } catch (e) { /* socket gone */ }
  }

  function buildHello(): string {
    const hello: HelloMsg = {
      type: 'hello',
      proto: 1,
      version: VERSION,
      client: opts.client ? opts.client : 'etunnel-harmonyos/' + VERSION
    };
    if (opts.subdomain) hello.subdomain = opts.subdomain;
    if (opts.token) hello.token = opts.token;
    if (opts.resume && opts.resume.length > 0) hello.resume = opts.resume;
    return JSON.stringify(hello);
  }

  function closeTunnel(reason: string): void {
    if (closed) return;
    closed = true;
    if (pinger !== 0) clearInterval(pinger);
    pinger = 0;
    trySend(JSON.stringify({ type: 'bye', reason: reason }));
    try {
      ws.close(1000, reason);
    } catch (e) { /* gone */ }
    if (opts.onClose) opts.onClose(reason);
  }

  ws.on('open', () => {
    if (openResolve) openResolve();
    trySend(buildHello());
  });
  ws.on('message', (err: Error, value: string | ArrayBuffer) => {
    lastActivity = Date.now();
    if (typeof value === 'string') {
      if (value.length > MAX_WS_MSG) {
        closeTunnel('read limit exceeded');
        return;
      }
      let m: Record<string, Object> | null = null;
      try {
        m = JSON.parse(value) as Record<string, Object>;
      } catch (e) { /* #243 M1: ignore malformed text frames */ }
      if (m === null) return;
      const type = m['type'] as string;
      if (!handshakeDone) {
        if (type === 'welcome') {
          handshakeDone = true;
          clearTimeout(handshakeTimer);
          if (welcomeResolve) welcomeResolve(m as Object as WelcomeMsg);
        } else if (type === 'error') {
          // Server error frame (protocol section 3). The server's message is
          // kept verbatim (client_outdated carries the upgrade URL); the code
          // is on ServerError.code so callers and the supervisor can act on it.
          const code = m['code'] as string;
          const message = m['message'] as string;
          clearTimeout(handshakeTimer);
          const se = new ServerError(code, message ? message : 'etunnel: server rejected: ' + code);
          if (welcomeReject) welcomeReject(se);
          return;
        }
        return;
      }
      if (type === 'req') {
        const id = m['streamId'] as number;
        // #243 M2: refuse a replayed live id or a stream beyond the cap.
        if (streams.has(id) || streams.size >= MAX_STREAMS) {
          trySend(ctlFrame(0x03, id));
          return;
        }
        const req = m as Object as ReqMsg;
        streams.set(id, { req: req, body: [], size: 0 });
      } else if (type === 'ping') {
        const pong: PongMsg = { type: 'pong', ts: m['ts'] as number };
        trySend(JSON.stringify(pong));
      } else if (type === 'pong') {
        lastPong = Date.now(); // #243 M3
      } else if (type === 'bye') {
        const bye = m as Object as ByeMsg;
        closed = true;
        if (pinger !== 0) clearInterval(pinger);
        pinger = 0;
        if (opts.onClose) opts.onClose(bye.reason ? bye.reason : 'server closed');
        try {
          ws.close(1000, 'bye');
        } catch (e) { /* gone */ }
      } else if (type === 'error') {
        // Mid-session server error: end the tunnel with the server's text.
        closeTunnel('server error: ' + (m['code'] as string) + ': ' + (m['message'] as string));
      }
      return;
    }
    // binary frame
    const ab = value as ArrayBuffer;
    if (ab.byteLength > MAX_WS_MSG) {
      closeTunnel('read limit exceeded');
      return;
    }
    if (ab.byteLength < 9) return;
    const f = parseFrame(ab);
    const st = streams.get(f.id);
    if (!st) return;
    if (f.kind === 0x01) {
      // #243 M2: bound the buffered request body (RST over the limit).
      const payload = new Uint8Array(ab, 9, Math.min(f.len, ab.byteLength - 9));
      st.size += payload.byteLength;
      if (st.size > MAX_STREAM_BODY) {
        streams.delete(f.id);
        trySend(ctlFrame(0x03, f.id));
        return;
      }
      st.body.push(payload);
    } else if (f.kind === 0x02) {
      streams.delete(f.id);
      // fire-and-forget: a crashing replay must not take the reader down
      handle(st, f.id).catch((e: Error) => {
        console.error('etunnel: replay sid=' + f.id + ' crashed: ' + (e && e.message ? e.message : String(e)));
      });
    } else if (f.kind === 0x03) {
      streams.delete(f.id);
    }
  });
  ws.on('close', () => {
    if (!handshakeDone) {
      const e = new Error('ws: closed during handshake');
      if (openReject) openReject(e);
      if (welcomeReject) welcomeReject(e);
      return;
    }
    closeTunnel('connection closed');
  });
  ws.on('error', () => {
    if (!handshakeDone) {
      const e = new Error('ws: connect failed');
      if (openReject) openReject(e);
      if (welcomeReject) welcomeReject(e);
      return;
    }
    closeTunnel('ws error');
  });

  // Subprotocol selection: protocols is the documented option on newer
  // API levels; the raw header is set as well so older stacks still pick
  // etunnel-v1 (the server tolerates either path).
  const connectOpts: webSocket.WebSocketRequestOptions = {};
  connectOpts.header = { 'Sec-WebSocket-Protocol': 'etunnel-v1' };
  connectOpts.protocols = ['etunnel-v1'];

  const opened = new Promise<void>((res, rej) => {
    openResolve = res;
    openReject = rej;
  });
  const connectErr: Error | null = await new Promise((res) => {
    ws.connect(server, connectOpts, (e: Error | null) => {
      // err === null means success (BusinessError-style OHOS callback).
      res(e === null ? null : new Error('ws: connect failed: ' + (e.message ? e.message : '')));
    });
  });
  if (connectErr !== null) {
    clearTimeout(handshakeTimer);
    throw connectErr;
  }
  await opened;
  if (handshakeDone) clearTimeout(handshakeTimer);

  const welcome = new Promise<WelcomeMsg>((res, rej) => {
    welcomeResolve = res;
    welcomeReject = rej;
  });
  const info: WelcomeMsg = await welcome;
  if (handshakeTimer) clearTimeout(handshakeTimer);

  function sendRes(id: number, status: number, headers: Record<string, string[]>): void {
    const res = { type: 'res', streamId: id, status: status, headers: headers };
    trySend(JSON.stringify(res));
  }

  // Local replay through @ohos.net.http (bodies buffered per stream).
  // #283-4: the whole path is wrapped - every send failure is caught and
  // answered with a best-effort RST, and the per-request http client is
  // destroyed on every path so no descriptor leaks.
  async function handle(st: StreamState, id: number): Promise<void> {
    const client = http.createHttp();
    try {
      const total = st.size;
      const body = new Uint8Array(total);
      let off = 0;
      for (const chunk of st.body) {
        body.set(chunk, off);
        off += chunk.byteLength;
      }
      const headerObj: Record<string, string> = {};
      const reqHeaders = st.req.headers;
      if (reqHeaders) {
        const keys = Object.keys(reqHeaders);
        for (const k of keys) {
          if (isHop(k)) continue;
          // #283-5 (documented limitation): the OHOS http header map is
          // single-valued, so multi-value headers are comma-joined; values
          // containing commas cannot round-trip. See README "Limits".
          headerObj[k] = reqHeaders[k].join(', ');
        }
      }
      let dialAddr = hp.host + ':' + hp.port;
      let dialPath: string = st.req.path;
      if (routes !== null) {
        const hit = routeFor(routes, dialPath);
        if (hit === null) {
          const notFound = '404 page not found (no route matches this path)\n';
          sendRes(id, 404, { 'Content-Type': ['text/plain; charset=utf-8'] });
          trySend(frame(0x01, id, utf8Encode(notFound)));
          trySend(ctlFrame(0x02, id));
          return;
        }
        dialAddr = hit.addr;
        dialPath = hit.path;
      }
      const reqOpts: http.HttpRequestOptions = {};
      reqOpts.method = httpMethodFor(st.req.method); // #283-2 passthrough
      reqOpts.header = headerObj;
      reqOpts.extraData = ['GET', 'HEAD'].indexOf(st.req.method) >= 0 ? undefined : body.buffer as ArrayBuffer;
      reqOpts.expectDataType = http.HttpDataType.ARRAY_BUFFER;
      reqOpts.usingCache = false;
      reqOpts.connectTimeout = 10000;
      reqOpts.readTimeout = 120000;
      let resp: http.HttpResponse;
      try {
        resp = await client.request('http://' + dialAddr + dialPath, reqOpts);
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        sendRes(id, 502, { 'Content-Type': ['text/html; charset=utf-8'] });
        trySend(frame(0x01, id, utf8Encode(localErrorPage(msg))));
        trySend(ctlFrame(0x02, id));
        return;
      }
      const rh: Record<string, string[]> = {};
      const headerKeys = Object.keys(resp.header as Object);
      for (const k of headerKeys) {
        if (isHop(k) || k.toLowerCase() === 'content-length' || k.toLowerCase() === 'transfer-encoding') continue;
        const v = (resp.header as Record<string, string>)[k];
        const arr: string[] = [v];
        rh[k] = arr;
      }
      sendRes(id, resp.responseCode, rh);
      const payload = new Uint8Array(resp.result as ArrayBuffer);
      for (let i = 0; i < payload.byteLength; i += MAXP) {
        trySend(frame(0x01, id, payload.subarray(i, Math.min(i + MAXP, payload.byteLength))));
      }
      trySend(ctlFrame(0x02, id));
    } catch (e) {
      // #283-4: forwarding failed mid-flight (socket died etc.) - answer with
      // a best-effort RST so the server does not wait on a ghost stream.
      const msg = e instanceof Error ? e.message : String(e);
      console.error('etunnel: replay sid=' + id + ' failed: ' + msg);
      try {
        ws.send(ctlFrame(0x03, id));
      } catch (e2) {
        console.error('etunnel: rst sid=' + id + ' failed: ' + (e2 instanceof Error ? e2.message : String(e2)));
      }
    } finally {
      client.destroy(); // #283-4: destroy on every path
    }
  }

  // #243 M3 keepalive: ping every 30s; dead after 150s without pongs or
  // traffic (NAT blackhole) so the supervisor can reconnect.
  pinger = setInterval(() => {
    if (closed) {
      clearInterval(pinger);
      pinger = 0;
      return;
    }
    const ping: PongMsg = { type: 'ping', ts: Math.floor(Date.now() / 1000) };
    trySend(JSON.stringify(ping));
    if (Date.now() - lastPong > PING_DEADLINE && Date.now() - lastActivity > PING_DEADLINE) {
      closeTunnel('ping timeout');
    }
  }, PING_INTERVAL);

  return {
    url: info.url,
    hostname: info.hostname,
    subdomain: info.subdomain,
    resumeToken: info.resumeToken ? info.resumeToken : '',
    plan: info.plan ? info.plan : '',
    close(reason?: string) {
      closeTunnel(reason ? reason : 'client closed');
    },
  };
}

// --- reconnect supervisor (ETUNNEL_RECONNECT=0 opts out) ----------------
const RETRY_MIN = 500;
const RETRY_MAX = 30000;

// Jittered exponential backoff: 500ms doubling to a 30s ceiling, +/-20%
// noise so a fleet of clients does not stampede after a server restart.
function retryDelay(attempt: number): number {
  const base = Math.min(RETRY_MIN * Math.pow(2, attempt), RETRY_MAX);
  return Math.round(base * (0.8 + Math.random() * 0.4));
}

function sleep(ms: number): Promise<void> {
  return new Promise<void>((res) => {
    setTimeout(res, ms);
  });
}

/** Cancellable supervisor handle (#283-3). */
export interface SupervisorHandle {
  /** Idempotent: stops the loop at the next boundary (drop, backoff sleep). */
  stop(): void;
  /** Resolves once the loop has fully exited. */
  done: Promise<void>;
}

/** A `error` frame from the server (protocol section 3). `message` is the
 * server's text verbatim - for `client_outdated` it names the required SDK
 * floor and the upgrade URL; `code` is the machine-readable error code. */
export class ServerError extends Error {
  code: string;

  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

// #283-3: a permanently invalid server URL must not retry forever.
const MAX_CONFIG_RETRIES = 3;

function isConfigError(msg: string): boolean {
  return /url|uri|scheme|invalid/i.test(msg);
}

async function runSupervise(server: string, local: string, opts: Options,
    dial: (server: string, local: string, opts: Options) => Promise<Tunnel>,
    isStopped: () => boolean, stopGate: Promise<void>): Promise<void> {
  let resume = '';
  let attempt = 0;
  let configFailures = 0;
  while (!isStopped()) {
    // Gate that resolves when the session ends (close(), server bye, drop),
    // forwarding to the embedder's own callback in the same breath.
    let gateResolve: ((reason: string) => void) | null = null;
    const gate = new Promise<string>((res) => {
      gateResolve = res;
    });
    const hook: Options = {
      routes: opts.routes,
      token: opts.token,
      subdomain: opts.subdomain,
      client: opts.client,
      resume: resume,
      onClose: (reason: string): void => {
        if (opts.onClose) opts.onClose(reason);
        if (gateResolve) gateResolve(reason);
      },
    };
    let sessionEnded = false;
    try {
      const t = await dial(server, local, hook);
      if (isStopped()) break;
      if (t.resumeToken.length > 0) resume = t.resumeToken;
      console.log('TUNNEL_URL: ' + t.url);
      attempt = -1; // a working session resets the backoff to 500ms
      await Promise.race([gate, stopGate]);
      sessionEnded = true;
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      console.error('etunnel: ' + msg);
      if (e instanceof ServerError && (e.code === 'client_outdated' || e.code === 'proto_version')) {
        // Permanent: this build is too old for the server - retrying cannot fix
        // it. The server's message (with the upgrade URL) was printed above.
        console.error('etunnel: supervisor stopped: ' + e.code + ' - upgrade the SDK');
        return;
      }
      if (isConfigError(msg)) {
        configFailures++;
        if (configFailures >= MAX_CONFIG_RETRIES) {
          console.error('etunnel: server URL failed ' + configFailures + 'x - giving up; fix ETUNNEL_SERVER/opts and restart the supervisor');
          return;
        }
      }
    }
    if (sessionEnded) console.error('etunnel: tunnel closed, reconnecting');
    if (isStopped()) break;
    const wait = retryDelay(attempt < 0 ? 0 : attempt);
    console.error('etunnel: retrying in ' + wait + 'ms');
    attempt++;
    await Promise.race([sleep(wait), stopGate]); // stop cuts the backoff short
  }
  console.error(isStopped() ? 'etunnel: supervisor stopped' : 'etunnel: supervisor exited');
}

/** Keep a tunnel up across drops; prints `TUNNEL_URL: <url>` on every
 *  successful connect (cross-SDK contract) and replays the resumeToken so
 *  the subdomain is reclaimed instead of re-randomised. Returns a handle:
 *  call `handle.stop()` to cancel (#283-3). Config-class failures (invalid
 *  server URL) are capped at 3 attempts, then the loop gives up. */
export function startSupervise(server: string, local: string, opts: Options = {},
    dial: (server: string, local: string, opts: Options) => Promise<Tunnel> = (server: string, local: string, o: Options) => connect(server, local, o)): SupervisorHandle {
  let stopped = false;
  let notifyStop: (() => void) | null = null;
  const stopGate = new Promise<void>((res) => {
    notifyStop = res;
  });
  const done = runSupervise(server, local, opts, dial, () => stopped, stopGate);
  const handle: SupervisorHandle = {
    stop(): void {
      stopped = true;
      if (notifyStop) notifyStop();
    },
    done: done,
  };
  return handle;
}
