// Minimal etunnel example for Deno:
//   deno run --allow-net example/basic.ts 3000
import { connect } from '../etunnel.ts';

const port = Number(Deno.args[0] || 3000);
Deno.serve({ port, onListen() { console.log(`local http://127.0.0.1:${port}`); } },
  () => new Response('hello from etunnel-example\n'));

const t = await connect('wss://t.etun.uk/tunnel', port);
console.log('public URL:', t.url);
