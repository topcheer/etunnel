// Test stub for `@ohos.net.websocket` (NOT the platform API - just enough of
// its shape for sdks/harmonyos/Etunnel.ets to run under Node, on top of
// Node's global WebSocket). Used by tests/run.sh to drive the real SDK source
// against the Go fixture server; see tests/hello.test.js.
class OhosWebSocket {
  constructor() {
    this.ws = null;
    this.handlers = {};
  }
  connect(url, options, cb) {
    try {
      const protocols = options && options.protocols ? options.protocols : undefined;
      this.ws = new WebSocket(url, protocols);
      this.ws.binaryType = 'arraybuffer';
      const emit = (type, ...args) => {
        for (const h of this.handlers[type] || []) h(...args);
      };
      this.ws.addEventListener('open', () => emit('open'));
      this.ws.addEventListener('message', (ev) => emit('message', null, ev.data));
      this.ws.addEventListener('close', (ev) => emit('close', { code: ev.code, reason: ev.reason }));
      this.ws.addEventListener('error', () => emit('error', new Error('ws error')));
      if (cb) cb(null);
    } catch (e) {
      if (cb) cb(e);
    }
  }
  on(type, cb) {
    (this.handlers[type] = this.handlers[type] || []).push(cb);
  }
  off(type) {
    delete this.handlers[type];
  }
  send(data) {
    this.ws.send(data);
  }
  close(code, reason) {
    this.ws.close(code, reason);
  }
}

module.exports.webSocket = { createWebSocket: () => new OhosWebSocket() };
