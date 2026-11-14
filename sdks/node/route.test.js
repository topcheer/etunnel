const test = require('node:test');
const assert = require('node:assert');
const { normalizeRoutes, routeFor } = require('./route');

test('segment boundaries + longest prefix', () => {
  const rs = normalizeRoutes([
    { prefix: '/', addr: 'fe:1' },
    { prefix: '/api', addr: 'be:1' },
    { prefix: '/api/v2', addr: 'be2:1' },
  ]);
  const want = { '/': 'fe:1', '/x': 'fe:1', '/apiv2': 'fe:1', '/apiary': 'fe:1', '/api': 'be:1', '/api/': 'be:1', '/api/u': 'be:1', '/api/v2': 'be2:1', '/api/v2/x?q': 'be2:1' };
  for (const [p, addr] of Object.entries(want)) assert.equal(routeFor(rs, p).addr, addr, p);
});

test('strip preserves query, bottoms at root, root-strip no-op', () => {
  const rs = normalizeRoutes([
    { prefix: '/api', addr: 'be:1', strip: true },
    { prefix: '/', addr: 'fe:1', strip: true },
  ]);
  assert.deepEqual(routeFor(rs, '/api/u?x=1&y=2'), { addr: 'be:1', path: '/u?x=1&y=2' });
  assert.equal(routeFor(rs, '/api').path, '/');
  assert.equal(routeFor(rs, '/api/').path, '/');
  assert.equal(routeFor(rs, '/app?c=1').path, '/app?c=1');
});

test('passthrough without strip', () => {
  const rs = normalizeRoutes([{ prefix: '/api', addr: 'be:1' }, { prefix: '/', addr: 'fe:1' }]);
  assert.equal(routeFor(rs, '/api/users?x=1').path, '/api/users?x=1');
});

test('no match returns null', () => {
  const rs = normalizeRoutes([{ prefix: '/api', addr: 'be:1' }]);
  assert.equal(routeFor(rs, '/other'), null);
  assert.notEqual(routeFor(rs, '/api/x'), null);
});

test('validation errors', () => {
  assert.throws(() => normalizeRoutes([{ prefix: '/', addr: '' }]), /Addr is required/);
  assert.throws(() => normalizeRoutes([{ addr: 'a:1' }]), /PathPrefix is required/);
  assert.throws(() => normalizeRoutes([{ prefix: 'api', addr: 'a:1' }]), /must start with/);
  assert.throws(() => normalizeRoutes([{ prefix: '/api', addr: 'a:1' }, { prefix: '/api/', addr: 'a:2' }]), /duplicate/);
});
