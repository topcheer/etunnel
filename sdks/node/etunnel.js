#!/usr/bin/env node
'use strict';
// etunnel SDK for Node.js (>= 22, built-in WebSocket, zero dependencies).
// Buffer-based protocol client: request/response bodies are buffered per
// stream (fine for typical webhooks and demos; WebSocket upgrades are not
// forwarded — use the Go SDK for full streaming/upgrades).
//
// const { connect } = require('./etunnel');
// const t = await connect('wss://t.ggcode.uk/tunnel', 3000);
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
  const ws = new WebSocket(server, 'etunnel-v1');
  ws.binaryType = 'arraybuffer';

  await new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = (e) => rej(new Error('ws: ' + (e.message || 'connect failed')));
  });

  ws.send(JSON.stringify({ type: 'hello', proto: 1, subdomain: opts.subdomain || undefined, token: opts.token || undefined, client: opts.client || 'etunnel-node/1.0' }));

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
      send(JSON.stringify({ type: 'res', streamId: id, status: 502, headers: { 'Content-Type': ['text/plain'] } }));
      sendBin(frame(0x01, id, Buffer.from('etunnel: local service error: ' + e.message + '\n')));
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

if (require.main === module) {
  const server = process.env.ETUNNEL_SERVER || 'wss://t.ggcode.uk/tunnel';
  const local = process.env.ETUNNEL_LOCAL || process.argv[2] || '3000';
  connect(server, local, { token: process.env.ETUNNEL_TOKEN, subdomain: process.env.ETUNNEL_SUBDOMAIN })
    .then((t) => { console.log('TUNNEL_URL: ' + t.url); })
    .catch((e) => { console.error('etunnel:', e.message); process.exit(1); });
}
