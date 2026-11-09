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
  onClose?: (reason: string) => void;
}

export interface Tunnel {
  url: string;
  hostname: string;
  subdomain: string;
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
      ws.send(JSON.stringify({ type: 'res', streamId: id, status: 502, headers: { 'Content-Type': ['text/plain'] } }));
      ws.send(frame(0x01, id, new TextEncoder().encode('etunnel: local service error: ' + (e as Error).message + '\n')));
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
