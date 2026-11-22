// #337-1 (HIGH): a close event racing the handshake rejection (proto gate /
// welcome timeout / error frame) must not TDZ-crash the embedder.
// State is hoisted before ws registration, so the async close lands safely.
(globalThis).process.argv[1] = ''; // keep the SDK's CLI auto-run block out of the test
const { connect } = require('../etunnel.js');

class FakeWS {
  constructor() {
    (globalThis).__lastFake = this;
    this.closed = false;
    this.onopen = null;
    this.onmessage = null;
    this.onclose = null;
    this.onerror = null;
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

process.on('uncaughtException', (e) => {
  console.log('FAIL: uncaught ReferenceError (TDZ not fixed):', e.message);
  process.exit(42);
});

(async () => {
  globalThis.WebSocket = FakeWS;
  try {
    await connect('wss://fake/tunnel', 3000);
    console.log('FAIL: proto=2 welcome was accepted');
    process.exit(1);
  } catch (e) {
    if (!/protocol v2/.test(e.message)) {
      console.log('FAIL: unexpected rejection:', e.message);
      process.exit(1);
    }
    // the async close event now lands; give it time to fire closeTunnel
    setTimeout(() => {
      console.log('PASS: handshake rejection with real close event - no TDZ crash');
      process.exit(0);
    }, 120);
  }
})();
