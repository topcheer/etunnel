// etunnel SDK for Deno (built-in WebSocket, zero dependencies).
// Buffer-based protocol client (see node/etunnel.js for the full notes).
//
// const t = await connect('wss://t.etun.uk/tunnel', 3000);
// console.log(t.url);

const MAXP = 32768;

function frame(kind: number, id: number, payload: Uint8Array): Uint8Array {
  const p = payload || new Uint8Array(0);
  const b = new Uint8Array(9 + p.length);
  b[0] = kind;
  new DataView(b.buffer).setUint32(1, id);
  new DataView(b.buffer).setUint32(5, p.length);
  b.set(p, 9);
  return b;
}

const HOP = new Set(['connection', 'proxy-connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade']);

export interface Options {
  token?: string;
  subdomain?: string;
  client?: string;
  /** Server-signed token from a previous session: reclaim the same subdomain. */
  resume?: string;
  onClose?: (reason: string) => void;
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

function pickHostPort(local: string | number): { host: string; port: number } {
  if (/^\d+$/.test(String(local))) return { host: '127.0.0.1', port: Number(local) };
  const m = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(local));
  if (m) return { host: m[1].replace(/[[\]]/g, ''), port: Number(m[2]) };
  return { host: String(local), port: 80 };
}

export async function connect(server: string, local: string | number, opts: Options = {}): Promise<Tunnel> {
  const { host, port } = pickHostPort(local);
  const ws = new WebSocket(server, 'etunnel-v1');
  ws.binaryType = 'arraybuffer'; // default is "blob" in Deno

  await new Promise<void>((res, rej) => {
    ws.onopen = () => res();
    ws.onerror = () => rej(new Error('ws: connect failed'));
  });

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain, token: opts.token, client: opts.client || 'etunnel-deno/1.0', resume: opts.resume || undefined }));

  const info = await new Promise<any>((res, rej) => {
    const to = setTimeout(() => rej(new Error('welcome timeout')), 15000);
    ws.onmessage = (ev) => {
      const m = JSON.parse(String(ev.data));
      if (m.type === 'welcome') { clearTimeout(to); res(m); }
      else if (m.type === 'error') { clearTimeout(to); rej(new Error(m.code + ': ' + (m.message || ''))); }
    };
  });

  const streams = new Map<number, { req: any; body: Uint8Array[] }>();
  let closed = false;

  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') {
      const m = JSON.parse(ev.data);
      if (m.type === 'req') streams.set(m.streamId, { req: m, body: [] });
      else if (m.type === 'ping') ws.send(JSON.stringify({ type: 'pong', ts: m.ts }));
      else if (m.type === 'bye') { closed = true; opts.onClose?.(m.reason); try { ws.close(); } catch { /* gone */ } }
      return;
    }
    const b = new Uint8Array(ev.data as ArrayBuffer);
    const kind = b[0];
    const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
    const id = dv.getUint32(1);
    const payload = b.subarray(9, 9 + dv.getUint32(5));
    const st = streams.get(id);
    if (!st) return;
    if (kind === 0x01) st.body.push(payload);
    else if (kind === 0x02) { streams.delete(id); handle(st, id); }
    else if (kind === 0x03) streams.delete(id);
  };

  async function handle(st: { req: any; body: Uint8Array[] }, id: number) {
    const body = concat(st.body);
    const headers: Record<string, string> = {};
    for (const [k, vs] of Object.entries(st.req.headers || {})) {
      if (HOP.has(k.toLowerCase())) continue;
      headers[k] = (vs as string[]).join(', ');
    }
    let resp: Response;
    try {
      resp = await fetch(`http://${host}:${port}${st.req.path}`, {
        method: st.req.method,
        headers,
        body: ['GET', 'HEAD'].includes(st.req.method) ? undefined : body,
      });
    } catch (e) {
      ws.send(JSON.stringify({ type: 'res', streamId: id, status: 502, headers: { 'Content-Type': ['text/html; charset=utf-8'] } }));
      ws.send(frame(0x01, id, new TextEncoder().encode(localErrorPage((e as Error).message))));
      ws.send(frame(0x02, id));
      return;
    }
    const rh: Record<string, string[]> = {};
    resp.headers.forEach((v, k) => { if (!HOP.has(k.toLowerCase())) (rh[k] = rh[k] || []).push(v); });
    ws.send(JSON.stringify({ type: 'res', streamId: id, status: resp.status, headers: rh }));
    const buf = new Uint8Array(await resp.arrayBuffer());
    for (let i = 0; i < buf.length; i += MAXP) ws.send(frame(0x01, id, buf.subarray(i, Math.min(i + MAXP, buf.length))));
    ws.send(frame(0x02, id));
  }

  function concat(parts: Uint8Array[]): Uint8Array {
    const n = parts.reduce((a, p) => a + p.length, 0);
    const out = new Uint8Array(n);
    let o = 0;
    for (const p of parts) { out.set(p, o); o += p.length; }
    return out;
  }

  return {
    url: info.url,
    hostname: info.hostname,
    subdomain: info.subdomain,
    resumeToken: info.resumeToken || '',
    plan: info.plan || '',
    close(reason = 'client closed') {
      if (closed) return;
      closed = true;
      try { ws.send(JSON.stringify({ type: 'bye', reason })); } catch { /* gone */ }
      try { ws.close(); } catch { /* gone */ }
      opts.onClose?.(reason);
    },
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
  const box: { ws: WebSocket | null } = { ws: null };
  const Real = globalThis.WebSocket as unknown as new (...args: unknown[]) => WebSocket;
  const Watch = function (...args: unknown[]): WebSocket {
    const s = new Real(...args);
    box.ws = s;
    return s;
  };
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = Watch;
  try {
    return { tunnel: await connect(server, local, opts), ws: box.ws };
  } finally {
    (globalThis as unknown as { WebSocket: unknown }).WebSocket = Real;
  }
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
    Deno.exit(0);
  };
  Deno.addSignalListener('SIGINT', () => shutdown('SIGINT'));
  Deno.addSignalListener('SIGTERM', () => shutdown('SIGTERM'));

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
      console.error('etunnel:', (e as Error).message);
    }
    const wait = retryDelay(attempt++);
    console.error('etunnel: retrying in ' + wait + 'ms');
    await new Promise<void>((res) => setTimeout(res, wait));
  }
}

if (import.meta.main) {
  const server = Deno.env.get('ETUNNEL_SERVER') || 'wss://t.etun.uk/tunnel';
  const local = Deno.env.get('ETUNNEL_LOCAL') || Deno.args[0] || '3000';
  const opts: Options = {
    token: Deno.env.get('ETUNNEL_TOKEN'),
    subdomain: Deno.env.get('ETUNNEL_SUBDOMAIN'),
  };
  try {
    if (Deno.env.get('ETUNNEL_RECONNECT') === '0') {
      // legacy: connect once, disconnect ends the process
      const t = await connect(server, local, opts);
      console.log('TUNNEL_URL: ' + t.url);
    } else {
      await supervise(server, local, opts);
    }
  } catch (e) {
    console.error('etunnel:', (e as Error).message);
    Deno.exit(1);
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
export interface Credentials {
  api_token: string;
  subdomains: string[] | null;
  plan: string;
  expires_at: number;
}

export interface LoginOptions {
  openBrowser?: (url: string) => void;
  timeoutMs?: number;
}

function defaultOpenBrowserDeno(u: string): void {
  const cmd = Deno.build.os === 'darwin' ? 'open' : Deno.build.os === 'windows' ? 'rundll32 url.dll,FileProtocolHandler' : 'xdg-open';
  new Deno.Command(cmd, { args: [u], stdout: 'null', stderr: 'null' }).spawn(); // fire-and-forget
}

export async function login(server: string, opts: LoginOptions = {}): Promise<Credentials> {
  const base = String(server || '').replace(/\/+$/, '');
  if (!base) throw new Error('etunnel: login: server required');
  const timeoutMs = opts.timeoutMs ?? 10 * 60 * 1000;
  let deviceCode = '';
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(new Error('etunnel: login timed out')), timeoutMs);

  const code = await new Promise<string>((resolve, reject) => {
    let settled = false;
    const srv = Deno.serve({ hostname: '127.0.0.1', port: 0, signal: ac.signal }, (req) => {
      const u = new URL(req.url);
      const c = u.searchParams.get('code');
      if (u.pathname !== '/callback' || !c) return new Response('not found', { status: 404 });
      settled = true;
      clearTimeout(timer);
      setTimeout(() => srv.unref(), 500); // let the response flush, then stop listening
      resolve(c);
      return new Response('<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>', { headers: { 'Content-Type': 'text/html; charset=utf-8' } });
    });
    srv.finished.catch((e) => { if (!settled) { clearTimeout(timer); reject(e); } });
    (async () => {
      try {
        const redirectUri = 'http://127.0.0.1:' + (srv.addr as { port: number }).port + '/callback';
        const r = await fetch(base + '/auth/cli/start', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ redirect_uri: redirectUri }), signal: ac.signal,
        });
        if (!r.ok) throw new Error('etunnel: login start: HTTP ' + r.status + ' ' + (await r.text()).slice(0, 200));
        const start = await r.json() as { auth_url?: string; device_code?: string };
        if (!start.auth_url || !start.device_code) throw new Error('etunnel: login: empty start payload');
        deviceCode = start.device_code;
        const open = opts.openBrowser || defaultOpenBrowserDeno;
        try { open(start.auth_url); } catch { /* headless: the URL is printed below */ }
        console.error('etunnel: authorize in your browser: ' + start.auth_url);
      } catch (e) { if (!settled) { clearTimeout(timer); try { srv.unref(); } catch { /* gone */ } reject(e); } }
    })();
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
