// --- Multi-target local routing (mirrors sdk/route.go semantics) --------
//
// One public hostname, several local services dispatched by URL path
// prefix. Longest prefix wins; matching is on segment boundaries ("/api"
// never matches "/apiv2"); StripPrefix removes only the path part (the
// query string is carried over byte-for-byte; an exact hit bottoms out at
// "/"); no match = caller answers 404.

/** @typedef {{prefix: string, addr: string, strip?: boolean}} Route */

/**
 * Validate and compile a route table: trims trailing slashes ("/" stays),
 * rejects duplicates and malformed prefixes, sorts longest-first.
 * @param {Route[]} routes
 * @returns {Route[]} the normalized table
 */
function normalizeRoutes(routes) {
  const out = [];
  const seen = new Set();
  for (let i = 0; i < routes.length; i++) {
    const r = routes[i];
    if (!r || typeof r.prefix !== 'string' || r.prefix === '') {
      throw new Error(`etunnel: route[${i}]: PathPrefix is required (use "/" for the fallback route)`);
    }
    if (!r.addr) throw new Error(`etunnel: route[${i}]: Addr is required`);
    let p = r.prefix;
    if (p[0] !== '/') throw new Error(`etunnel: route[${i}]: PathPrefix "${p}" must start with '/'`);
    while (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1);
    if (seen.has(p)) throw new Error(`etunnel: route[${i}]: duplicate PathPrefix "${p}"`);
    seen.add(p);
    out.push({ prefix: p, addr: r.addr, strip: !!r.strip });
  }
  out.sort((a, b) => b.prefix.length - a.prefix.length);
  return out;
}

/**
 * Resolve "path?query" against the table. Returns { addr, path } or null.
 * @param {Route[]} routes normalized table
 * @param {string} pathAndQuery
 */
function routeFor(routes, pathAndQuery) {
  const qi = pathAndQuery.indexOf('?');
  const path = qi >= 0 ? pathAndQuery.slice(0, qi) : pathAndQuery;
  const query = qi >= 0 ? pathAndQuery.slice(qi) : '';
  for (const r of routes) {
    if (!(r.prefix === '/' || path === r.prefix || path.startsWith(r.prefix + '/'))) continue;
    let p = path;
    if (r.strip) {
      p = r.prefix === '/' ? path : (path === r.prefix ? '/' : path.slice(r.prefix.length));
    }
    return { addr: r.addr, path: p + query };
  }
  return null;
}

if (typeof module !== 'undefined') module.exports = { normalizeRoutes, routeFor };
