// #330-2: welcome.proto=2 must be rejected loudly (proto gate).
// Runs under Node (>=22.18 type stripping):  node tests/proto_gate.ts

class FakeWS {
  sent: string[] = [];
  closed = false;
  opened = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: ((e: Error) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    (globalThis as any).__lastFake = this;
    setTimeout(() => {
      this.opened = true;
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

(globalThis as any).process.argv[1] = ''; // keep the SDK's CLI auto-run block out of the test
(globalThis as any).WebSocket = FakeWS;

const { connect } = await import('../etunnel.ts');
setTimeout(() => {
  const f = (globalThis as any).__lastFake as FakeWS;
  f.onmessage && f.onmessage({ data: JSON.stringify({ type: 'welcome', proto: 2, url: 'https://f.t.test', hostname: 'f', subdomain: 'f', plan: 'free' }) });
}, 30);
try {
  await connect('wss://fake/tunnel', 3000);
  console.log('FAIL: proto=2 welcome was accepted');
  process.exit(1);
} catch (e) {
  const fake = (globalThis as any).__lastFake as FakeWS;
  if (e instanceof Error && /protocol v2/.test(e.message) && fake.closed) {
    console.log('PASS: welcome.proto=2 rejected and socket closed:', e.message);
    process.exit(0);
  }
  console.log('FAIL: unexpected:', (e as Error).message);
  process.exit(1);
}
