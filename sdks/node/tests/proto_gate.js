// #322-6: welcome.proto=2 must be rejected loudly (proto gate).
const { connect } = require('../etunnel.js');

class FakeWS {
  constructor() {
    this.sent = [];
    this.closed = false;
    this.onopen = null;
    this.onmessage = null;
    this.onclose = null;
    this.onerror = null;
    FakeWS.last = this;
    setTimeout(() => {
      this.onopen && this.onopen();
    }, 5);
  }
  send(d) { this.sent.push(d); return true; }
  close() { this.closed = true; }
}
FakeWS.last = null;

(async () => {
  globalThis.WebSocket = FakeWS;
  const p = connect('wss://fake/tunnel', 3000);
  await new Promise((r) => setTimeout(r, 30)); // onopen fired, hello sent, onmessage bound
  FakeWS.last.onmessage({
    data: JSON.stringify({ type: 'welcome', proto: 2, url: 'https://f.t.test', hostname: 'f', subdomain: 'f', plan: 'free' }),
  });
  try {
    await p;
    console.log('FAIL: proto=2 welcome was accepted');
    process.exit(1);
  } catch (e) {
    if (/protocol v2/.test(e.message) && FakeWS.last.closed) {
      console.log('PASS: welcome.proto=2 rejected and socket closed:', e.message);
      process.exit(0);
    }
    console.log('FAIL: unexpected error:', e.message);
    process.exit(1);
  }
})();
