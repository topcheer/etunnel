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

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain, token: opts.token, client: opts.client || 'etunnel-deno/1.0' }));

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

if (import.meta.main) {
  const server = Deno.env.get('ETUNNEL_SERVER') || 'wss://t.etun.uk/tunnel';
  const local = Deno.env.get('ETUNNEL_LOCAL') || Deno.args[0] || '3000';
  try {
    const t = await connect(server, local, {
      token: Deno.env.get('ETUNNEL_TOKEN'),
      subdomain: Deno.env.get('ETUNNEL_SUBDOMAIN'),
    });
    console.log('TUNNEL_URL: ' + t.url);
  } catch (e) {
    console.error('etunnel:', (e as Error).message);
    Deno.exit(1);
  }
}
