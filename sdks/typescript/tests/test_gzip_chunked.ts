// #286 regression: a local backend answering gzip with chunked transfer
// framing must reach the tunnel as the pure gzip entity - the replay stack
// de-frames transport bytes, and Transfer-Encoding / stale Content-Length
// never cross the wire (PROTOCOL.md §2 identity-body invariant).
//
// Run: node --experimental-strip-types tests/test_gzip_chunked.ts
// Prints PASS on success, exits non-zero on failure. No network: the tunnel
// socket is a fake injected via wsFactory, only the local HTTP hop is real.
//
// etunnel.ts auto-dispatches its CLI supervisor when imported (argv[1]
// guard is always true), which would dial the default production server.
// Neutralize argv BEFORE the dynamic import so the test stays offline.
process.argv[1] = "";
export {};
const { connect } = await import("../etunnel.ts");
const { gzipSync } = await import("node:zlib");
const http = (await import("node:http")).default;

const WELCOME = JSON.stringify({ type: "welcome", url: "https://x.t.test", plan: "free" });

const original = Buffer.from("etunnel #286 identity-body payload - ".repeat(42));
const gz = gzipSync(original);

// Local backend: gzip entity, no Content-Length, flushed chunks => chunked
// transfer framing on the local hop (NanoHTTPD-class behavior).
const local = http.createServer((req: any, res: any) => {
  res.setHeader("Content-Encoding", "gzip");
  res.setHeader("Content-Type", "text/html; charset=utf-8");
  res.writeHead(200);
  for (let i = 0; i < gz.length; i += 100) {
    res.write(gz.subarray(i, Math.min(i + 100, gz.length)));
    (res as any).flush?.();
  }
  res.end();
});
await new Promise<void>((ok) => local.listen(0, "127.0.0.1", ok));
const port = (local.address() as any).port;

// Fake tunnel socket: full SDK replay path runs against it; every ws.send
// is captured for the wire assertions below. Welcome is delivered
// reactively once the SDK sends hello (the handshake await cannot be
// satisfied by the test body, which is suspended inside connect()).
class FakeWS {
  binaryType = "arraybuffer";
  sent: Array<string | Buffer> = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null;
  onerror: ((e: any) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    queueMicrotask(() => this.onopen?.());
  }
  send(d: string | Uint8Array): boolean {
    const buf = typeof d === "string" ? d : Buffer.from(d);
    this.sent.push(buf);
    if (typeof buf === "string" && buf.includes('"hello"')) {
      queueMicrotask(() => this.onmessage?.({ data: WELCOME }));
    }
    return true;
  }
  close() { this.onclose?.(); return true; }
}

function fail(msg: string): never {
  console.log(`FAIL: ${msg}`);
  local.close();
  process.exit(1);
}

let ws: FakeWS;
const tun = await connect("ws://injected.invalid/tunnel", port, {
  wsFactory: () => (ws = new FakeWS()) as any,
});

// Pump messages the "server" would deliver into the SDK.
const pump = (data: string | ArrayBuffer) => ws.onmessage?.({ data });

// Request + FIN (9-byte frame header: kind|sid|len).
pump(JSON.stringify({ type: "req", streamId: 1, method: "GET", path: "/index.html", headers: { "Accept-Encoding": ["gzip"] } }));
const fin = Buffer.alloc(9);
fin[0] = 0x02; fin.writeUInt32BE(1, 1);
pump(fin.buffer.slice(fin.byteOffset, fin.byteOffset + fin.byteLength));

// Give the replay path a tick to fetch + send res/body/fin.
await new Promise((ok) => setTimeout(ok, 300));

const resMsg = ws.sent.find((m) => typeof m === "string" && m.includes('"type":"res"'));
if (!resMsg) fail("no res sent");
const res = JSON.parse(resMsg as string);
const hdrs = Object.fromEntries(Object.entries(res.headers || {}).map(([k, v]: [string, any]) => [k.toLowerCase(), v]));
if (hdrs["content-encoding"]?.[0] !== "gzip") fail(`Content-Encoding lost: ${JSON.stringify(res.headers)}`);
if ("transfer-encoding" in hdrs) fail(`Transfer-Encoding crossed the wire: ${JSON.stringify(res.headers)}`);
if ("content-length" in hdrs) fail(`Content-Length crossed the wire: ${JSON.stringify(res.headers)}`);

const dataFrames = (ws.sent as Buffer[]).filter((m) => Buffer.isBuffer(m) && m.length >= 9 && m[0] === 0x01 && m.readUInt32BE(1) === 1);
const body = Buffer.concat(dataFrames.map((f) => f.subarray(9, 9 + f.readUInt32BE(5))));
if (!body.equals(gz)) {
  fail(`DATA bytes != pure gzip entity (got ${body.length}B starting ${body.subarray(0, 4).toString("hex")}; want ${gz.length}B starting 1f8b)`);
}
const finSent = (ws.sent as Buffer[]).some((m) => Buffer.isBuffer(m) && m.length === 9 && m[0] === 0x02 && m.readUInt32BE(1) === 1);
if (!finSent) fail("no FIN after body");

console.log(`PASS: gzip+chunked replay stays identity on the wire (${body.length}B gzip, TE/CL stripped, CE preserved)`);
local.close();
tun.close("test done");
process.exit(0);
