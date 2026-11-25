// hello.version + client_outdated: the SDK reports its version in hello and
// surfaces the server's client_outdated message verbatim (with upgrade URL).
(globalThis).process.argv[1] = ''; // keep the SDK's CLI auto-run block out of the test
const { connect, VERSION } = await import('../etunnel.ts');

class FakeWS {
  constructor() {
    this.sent = [];
    this.closed = false;
    this.onopen = null;
    this.onmessage = null;
    this.onclose = null;
    this.onerror = null;
    FakeWS.last = this;
    setTimeout(() => this.onopen && this.onopen(), 5);
  }
  send(d) { this.sent.push(d); return true; }
  close() { this.closed = true; }
}

const OUTDATED = 'etunnel client 1.0.0 is too old; this server runs 9.9.9 and requires SDK >= 1.7.0. Upgrade: https://github.com/topcheer/etunnel';

(async () => {
  let fails = 0;
  const ck = (n, got, want) => { if (got !== want) { fails++; console.log(`FAIL ${n}: got ${got} want ${want}`); } };

  // 1) hello carries version and the derived client string
  globalThis.WebSocket = FakeWS;
  const p1 = connect('wss://fake/tunnel', 3000);
  await new Promise((r) => setTimeout(r, 30));
  const hello = JSON.parse(FakeWS.last.sent[0]);
  ck('hello.type', hello.type, 'hello');
  ck('hello.version === VERSION', hello.version, VERSION);
  ck('hello.version literal', hello.version, '1.7.39');
  ck('client derived from VERSION', hello.client, `etunnel-ts/${VERSION}`);
  FakeWS.last.onmessage({ data: JSON.stringify({ type: 'welcome', proto: 1, url: 'https://f', hostname: 'f', subdomain: 'f', plan: '' }) });
  await p1;

  // 2) client_outdated: message (with URL) reaches the rejection, code attached
  const p2 = connect('wss://fake/tunnel', 3000);
  await new Promise((r) => setTimeout(r, 30));
  FakeWS.last.onmessage({ data: JSON.stringify({ type: 'error', code: 'client_outdated', message: OUTDATED }) });
  try {
    await p2;
    fails++; console.log('FAIL: client_outdated was accepted');
  } catch (e) {
    ck('code attached', e.code, 'client_outdated');
    ck('message verbatim incl. URL', e.message.includes(OUTDATED), true);
    ck('upgrade URL present', e.message.includes('https://github.com/topcheer/etunnel'), true);
  }

  if (fails) { console.log(`ts hello.version tests: FAIL (${fails})`); process.exit(1); }
  console.log('ts hello.version tests: PASS');
  process.exit(0);
})();
