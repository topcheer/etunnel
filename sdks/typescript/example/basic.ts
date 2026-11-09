// Minimal etunnel example (TypeScript). Sources are not on npm yet:
// copy ../etunnel.ts next to this file, then run with Node >= 22:
//   node example/basic.ts 3000
import * as http from 'node:http';
import { connect } from './etunnel.ts';

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
