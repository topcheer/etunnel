'use strict';
// Wire tests for the HarmonyOS SDK under Node. run.sh compiles the real
// Etunnel.ets (TypeScript, `@ohos.*` replaced by the stubs in ../stubs) to
// out/Etunnel.js and points SDK_JS at it; the peer is the Go fixture server
// sdks/c/testtools/wssserver.
//
//  1. hello.version: the hello carries exactly the SDK's VERSION constant
//  2. error client_outdated: connect() rejects with ServerError (code +
//     verbatim message) and startSupervise() gives up instead of retrying
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');

const sdk = require(process.env.SDK_JS);
const SDK_VERSION = process.env.SDK_VERSION;
const FIXTURE_BIN = process.env.FIXTURE_BIN;
const OUTDATED_MSG =
  'etunnel client 0.0.1 is too old; this server runs 9.9.9 and requires SDK >= 1.0.0. ' +
  'Upgrade: https://etun.uk';

function sleep(ms) {
  return new Promise((res) => setTimeout(res, ms));
}

// Start the fixture; resolves once it printed its listen address.
function startFixture(mode, extraEnv) {
  return new Promise((resolve, reject) => {
    const child = spawn(FIXTURE_BIN, ['-mode', mode, '-listen', '127.0.0.1:0', '-plain'], {
      env: Object.assign({}, process.env, extraEnv),
      stdio: ['ignore', 'pipe', 'pipe']
    });
    let out = '';
    let settled = false;
    const timer = setTimeout(() => {
      if (!settled) reject(new Error('fixture did not start: ' + out));
      child.kill();
    }, 15000);
    child.stdout.on('data', (d) => {
      out += d.toString();
      if (!settled && out.indexOf('\n') >= 0) {
        settled = true;
        clearTimeout(timer);
        resolve({ child: child, addr: out.split('\n')[0].trim(), output: () => out });
      }
    });
    child.stderr.on('data', (d) => {
      out += d.toString();
    });
    child.on('error', (e) => {
      if (!settled) reject(e);
    });
  });
}

async function waitForOutput(f, needle) {
  for (let i = 0; i < 80; i++) {
    if (f.output().indexOf(needle) >= 0) return;
    await sleep(50);
  }
  throw new Error('fixture never printed ' + needle + '; got: ' + f.output());
}

async function testHelloVersion() {
  const f = await startFixture('hellocheck', { ET_TEST_VERSION: SDK_VERSION });
  try {
    const t = await sdk.connect('ws://' + f.addr + '/tunnel', '3000');
    assert.equal(t.url, 'http://fake.t.test', 'welcome url');
    await waitForOutput(f, 'HELLO_VERSION_OK');
    assert.ok(f.output().indexOf('HELLO_VERSION_OK ' + SDK_VERSION) >= 0, f.output());
    assert.equal(f.output().indexOf('HELLO_BAD'), -1, f.output());
    t.close('done');
    console.log('PASS: hello.version == ' + SDK_VERSION + ' (fixture: ' +
      f.output().split('\n').filter((l) => l.indexOf('HELLO_') === 0).join(' | ') + ')');
  } finally {
    f.child.kill();
  }
}

async function testOutdated() {
  const f = await startFixture('outdated', { ET_TEST_OUTDATED_MSG: OUTDATED_MSG });
  const errors = [];
  const origError = console.error;
  console.error = (...args) => errors.push(args.join(' '));
  try {
    let rejected = null;
    try {
      await sdk.connect('ws://' + f.addr + '/tunnel', '3000');
    } catch (e) {
      rejected = e;
    }
    assert.ok(rejected, 'connect() must reject on client_outdated');
    assert.ok(rejected instanceof sdk.ServerError, 'ServerError, got ' + rejected);
    assert.equal(rejected.code, 'client_outdated');
    assert.equal(rejected.message, OUTDATED_MSG, 'message verbatim');

    // The supervisor treats it as permanent: it prints the server's message
    // and stops instead of re-dialing.
    const handle = sdk.startSupervise('ws://' + f.addr + '/tunnel', '3000', {});
    await Promise.race([
      handle.done,
      sleep(8000).then(() => {
        throw new Error('supervisor did not stop on client_outdated');
      })
    ]);
    assert.ok(errors.some((l) => l === 'etunnel: ' + OUTDATED_MSG), 'verbatim message on console.error: ' + errors);
    assert.ok(errors.some((l) => l.indexOf('supervisor stopped: client_outdated') >= 0), errors);
    console.log('PASS: client_outdated -> ServerError(code=client_outdated), message verbatim, supervisor stopped');
  } finally {
    console.error = origError;
    f.child.kill();
  }
}

(async () => {
  assert.equal(typeof SDK_VERSION, 'string');
  await testHelloVersion();
  await testOutdated();
  process.exit(0);
})().catch((e) => {
  console.error('FAIL: ' + (e && e.stack ? e.stack : e));
  process.exit(1);
});
