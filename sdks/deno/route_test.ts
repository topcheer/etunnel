// Mirror of the shared semantics table (see node/route.test.js).
import { normalizeRoutes, routeFor } from './etunnel.ts';
import { assert, assertThrows, assertEquals } from 'jsr:@std/assert';

Deno.test('boundaries + longest', () => {
  const rs = normalizeRoutes([{ prefix: '/', addr: 'fe:1' }, { prefix: '/api', addr: 'be:1' }, { prefix: '/api/v2', addr: 'be2:1' }]);
  assertEquals(routeFor(rs, '/apiv2')!.addr, 'fe:1');
  assertEquals(routeFor(rs, '/apiary')!.addr, 'fe:1');
  assertEquals(routeFor(rs, '/api')!.addr, 'be:1');
  assertEquals(routeFor(rs, '/api/u')!.addr, 'be:1');
  assertEquals(routeFor(rs, '/api/v2/x?q')!.addr, 'be2:1');
});
Deno.test('strip semantics', () => {
  const rs = normalizeRoutes([{ prefix: '/api', addr: 'be:1', strip: true }, { prefix: '/', addr: 'fe:1', strip: true }]);
  assertEquals(routeFor(rs, '/api/u?x=1&y=2'), { addr: 'be:1', path: '/u?x=1&y=2' });
  assertEquals(routeFor(rs, '/api')!.path, '/');
  assertEquals(routeFor(rs, '/app?c=1')!.path, '/app?c=1');
});
Deno.test('no match', () => {
  const rs = normalizeRoutes([{ prefix: '/api', addr: 'be:1' }]);
  assertEquals(routeFor(rs, '/other'), null);
});
Deno.test('validation', () => {
  assertThrows(() => normalizeRoutes([{ prefix: '/', addr: '' }]), Error, 'Addr is required');
  assertThrows(() => normalizeRoutes([{ prefix: 'api', addr: 'a:1' }]), Error, "must start with");
  assertThrows(() => normalizeRoutes([{ prefix: '/api', addr: 'a:1' }, { prefix: '/api/', addr: 'a:2' }]), Error, 'duplicate');
});
