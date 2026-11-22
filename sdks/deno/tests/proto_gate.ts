// #330-2: welcome.proto=2 must be rejected loudly (proto gate).
// Runs under Deno:  deno run --allow-none tests/proto_gate.ts
import { connect } from '../etunnel.ts';

class FakeWS {
  sent: string[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: ((e: Error) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    (globalThis as any).__lastFake = this;
    setTimeout(() => {
      this.onopen && this.onopen();
    }, 5);
  }
  send(d: string) {
    this.sent.push(d);
    return true;
  }
  close() {
    this.closed = true;
  }
}

const origWS = globalThis.WebSocket;
(globalThis as any).WebSocket = FakeWS;

const p = connect('wss://fake/tunnel', 3000);
setTimeout(() => {
  (globalThis as any).__lastFake.onmessage &&
    (globalThis as any).__lastFake.onmessage({ data: JSON.stringify({ type: 'welcome', proto: 2, url: 'https://f.t.test', hostname: 'f', subdomain: 'f', plan: 'free' }) });
}, 30);
try {
  await p;
  console.log('FAIL: proto=2 welcome was accepted');
  (globalThis as any).Deno.exit(1);
} catch (e) {
  const fake = (globalThis as any).__lastFake as FakeWS;
  if (e instanceof Error && /protocol v2/.test(e.message) && fake.closed) {
    console.log('PASS: welcome.proto=2 rejected and socket closed:', e.message);
    (globalThis as any).Deno.exit(0);
  }
  console.log('FAIL: unexpected:', (e as Error).message);
  (globalThis as any).Deno.exit(1);
}
