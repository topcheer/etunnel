// Verify the CLI-exchanged API key: bring up a real tunnel and serve one request.
const { connect } = require('../etunnel');
const KEY = process.argv[2];
const http = require('http');

(async () => {
  const local = http.createServer((req, res) => res.end('KEY-VERIFIED-OK'));
  await new Promise(r => local.listen(0, '127.0.0.1', r));
  const port = local.address().port;
  const t = await connect('wss://t.etun.uk/tunnel', '127.0.0.1:' + port, { token: KEY });
  console.log('[verify] tunnel up:', t.url);
  const pub = t.url.replace(/^https?:\/\//, '');
  const r = await fetch('https://' + pub + '/ping');
  const body = await r.text();
  console.log('[verify] via-tunnel HTTP', r.status, body);
  await t.close(); local.close(); process.exit(r.status === 200 && body === 'KEY-VERIFIED-OK' ? 0 : 1);
})().catch(e => { console.error('[verify] FAIL:', e.message); process.exit(1); });
