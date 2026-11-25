// Test stub for `@ohos.net.http` (NOT the platform API). The hello tests never
// replay a request (the fixture sends only a welcome), so every request fails
// fast with a 502-shaped result; see tests/stubs/@ohos.net.websocket/index.js.
function createHttp() {
  return {
    request(_options, cb) {
      const fail = { responseCode: 502, header: {}, result: '' };
      if (cb) cb(fail);
      return Promise.resolve(fail);
    },
    destroy() {}
  };
}

module.exports.http = {
  createHttp,
  // Enum the SDK references at replay time (never reached by the hello tests).
  HttpDataType: { STRING: 0, OBJECT: 1, ARRAY_BUFFER: 2 }
};
