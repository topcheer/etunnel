// Regression (docs audit, P1): importing sdks/typescript/etunnel.ts must NOT
// start the CLI supervisor. The old "run directly" check
//   process.argv[1].endsWith(process.argv[1].split('/').pop())
// was a tautology (x.endsWith(lastSegment(x)) is always true), so any program
// that imported the file got a SECOND tunnel from the supervisor - which also
// installed its own SIGINT handler on the importer's process. The existing
// tests worked around it by blanking process.argv[1] before importing.
//
// What this does: spawns tests/import_no_cli_child.ts (which imports the SDK
// and opens exactly one tunnel) against the local harness, asserts that no
// "TUNNEL_URL:" line (the supervisor's stdout) ever appears, probes the one
// tunnel the child announced, then sends a single SIGINT and requires the
// child to close its tunnel and exit 0.
//
// Run (harness up, see /tmp/et-harness.sh):
//   node sdks/typescript/tests/import_no_cli.ts
// Exit 77 = harness not reachable (skip).
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import http from 'node:http';

const server = process.env.ETUNNEL_SERVER || 'ws://localhost:8080/tunnel';
const local = process.env.ETUNNEL_LOCAL || '3000';
const childPath = fileURLToPath(new URL('./import_no_cli_child.ts', import.meta.url));

function get(host: string, path = '/'): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port: 8080, path, headers: { Host: host }, timeout: 8000 }, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (c) => (body += c));
      res.on('end', () => resolve({ status: res.statusCode || 0, body }));
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('timeout')));
    req.end();
  });
}

let childProc: ReturnType<typeof spawn> | undefined;

function fail(msg: string): never {
  console.error('FAIL: ' + msg);
  childProc?.kill('SIGKILL');
  process.exit(1);
}

// harness reachable?
try {
  const r = await get('localhost');
  if (r.status !== 200) fail('harness answered ' + r.status + ' on the base host');
} catch (e) {
  console.log('SKIP: harness not reachable (' + (e as Error).message + ')');
  process.exit(77);
}

const child = spawn(process.execPath, [childPath], {
  env: { ...process.env, ETUNNEL_SERVER: server, ETUNNEL_LOCAL: local },
  stdio: ['ignore', 'pipe', 'pipe'],
});
childProc = child;
let out = '';
child.stdout.on('data', (d) => (out += String(d)));
let err = '';
child.stderr.on('data', (d) => (err += String(d)));

// 1. the child announces its own tunnel on APP_TUNNEL:
const deadline = Date.now() + 20000;
while (!out.includes('APP_TUNNEL: ') && Date.now() < deadline) {
  if (child.exitCode !== null) fail('child exited early code=' + child.exitCode + ' stderr=' + err);
  await new Promise((r) => setTimeout(r, 100));
}
const m = out.match(/APP_TUNNEL: (http:\/\/[^\s]+)/);
if (!m) fail('child never announced its tunnel; stderr=' + err);
const url = new URL(m![1]);
const host = url.host;
console.log('child tunnel: ' + m![1]);

// 2. give a wrongly-started supervisor time to announce ITS tunnel
await new Promise((r) => setTimeout(r, 2500));
const supervisorLines = out.split('\n').filter((l) => l.startsWith('TUNNEL_URL:'));
if (supervisorLines.length !== 0) {
  fail('importing the SDK started the CLI supervisor (' + supervisorLines.length + ' TUNNEL_URL line(s)): ' + supervisorLines.join(' | '));
}
console.log('no supervisor TUNNEL_URL after import: OK');

// 3. the one tunnel works
const probe = await get(host, '/');
if (!probe.body.includes('ET-LOCAL-OK')) fail('probe through the child tunnel: status=' + probe.status + ' body=' + probe.body.slice(0, 80));
console.log('probe through the child tunnel: OK');

// 4. a single SIGINT closes the child's tunnel and exits 0 - the supervisor
//    used to register its own SIGINT handler on the importer's process
child.kill('SIGINT');
const exitDeadline = Date.now() + 8000;
while (child.exitCode === null && Date.now() < exitDeadline) await new Promise((r) => setTimeout(r, 100));
if (child.exitCode === null) {
  child.kill('SIGKILL');
  fail('child did not exit within 8s of SIGINT (SIGINT handler stolen?)');
}
if (child.exitCode !== 0) fail('child exit code ' + child.exitCode + ' (want 0); stderr=' + err);
if (!out.includes('APP_CLOSED')) fail('child did not run its own SIGINT handler (APP_CLOSED missing)');
await new Promise((r) => setTimeout(r, 800));
const after = await get(host, '/');
if (after.status !== 404) fail('tunnel still up after close: status=' + after.status);
console.log('PASS: import starts no supervisor; one tunnel; one SIGINT closes it and exits 0 (tunnel -> 404)');
