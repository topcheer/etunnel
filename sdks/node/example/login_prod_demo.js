// Local demo: CLI login against PRODUCTION, then verify the key with a tunnel.
const { login, connect } = require('../etunnel');

const SERVER = process.argv[2] || 'https://t.etun.uk';

(async () => {
  console.log('[demo] starting CLI login against', SERVER);
  const creds = await login(SERVER, { timeoutMs: 600000 });
  console.log('[demo] EXCHANGED key len=' + creds.api_token.length,
    'subdomains=' + JSON.stringify(creds.subdomains), 'plan=' + creds.plan);

  // verify the key actually works: bring up a 30s tunnel
  const t = await connect({ server: SERVER, apiKey: creds.api_token });
  console.log('[demo] tunnel URL:', t.url);
  setTimeout(async () => { await t.close(); console.log('[demo] done'); process.exit(0); }, 30000);
})().catch(e => { console.error('[demo] FAIL:', e.message); process.exit(1); });
