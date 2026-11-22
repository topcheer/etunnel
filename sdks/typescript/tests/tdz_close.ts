// #337-1 (HIGH): a close event racing the handshake rejection (proto gate /
// welcome timeout / error frame) must not TDZ-crash the embedder.
// Runs under node >=22.18 (type stripping):  node tests/tdz_close.ts

class FakeWS {
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: ((e: Error) => void) | null = null;
  constructor() {
    (globalThis as any).__lastFake = this;
    setTimeout(() => {
      this.onopen && this.onopen();
      setTimeout(() => {
        // the SDK binds its welcome handler in the onopen continuation
        this.onmessage && this.onmessage({ data: JSON.stringify({ type: 'welcome', proto: 2, url: 'https://f.t.test', hostname: 'f', subdomain: 'f', plan: 'free' }) });
      }, 15);
    }, 5);
  }
  send() { return true; }
  close() {
    this.closed = true;
    setTimeout(() => { this.onclose && this.onclose({ code: 1000 }); }, 20);
  }
}

(globalThis as any).process.argv[1] = ''; // keep the SDK's CLI auto-run block out of the test
(globalThis as any).WebSocket = FakeWS;
(globalThis as any).process.on('uncaughtException', (e: Error) => {
  console.log('FAIL: uncaught ReferenceError (TDZ not fixed):', e.message);
  (globalThis as any).process.exit(42);
});

const { connect } = await import('../etunnel.ts');
try {
  await connect('wss://fake/tunnel', 3000);
  console.log('FAIL: proto=2 welcome was accepted');
  (globalThis as any).process.exit(1);
} catch (e) {
  if (!(e instanceof Error) || !/protocol v2/.test(e.message)) {
    console.log('FAIL: unexpected rejection:', (e as Error).message);
    (globalThis as any).process.exit(1);
  }
  setTimeout(() => {
    console.log('PASS: handshake rejection with real close event - no TDZ crash');
    (globalThis as any).process.exit(0);
  }, 120);
}
