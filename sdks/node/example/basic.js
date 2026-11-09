// Minimal etunnel example: serve a local HTTP page through a public tunnel.
//   node example/basic.js            # local port 3000
//   node example/basic.js 8080       # custom local port
const http = require('http');
const { connect } = require('../etunnel.js');

const port = Number(process.argv[2] || 3000);
const server = http.createServer((req, res) => {
  res.writeHead(200, { 'content-type': 'text/plain' });
  res.end(`hello from etunnel-example (${req.method} ${req.url})\n`);
});

server.listen(port, async () => {
  console.log(`local http://127.0.0.1:${port}`);
  const t = await connect('wss://t.etun.uk/tunnel', port);
  console.log('public URL:', t.url);
});
