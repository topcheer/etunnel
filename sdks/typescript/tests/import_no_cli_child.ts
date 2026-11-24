// Child of tests/import_no_cli.ts: imports the SDK the way an application
// does (relative import of etunnel.ts) and opens exactly ONE tunnel. With the
// old tautological "run directly" check this import also started the CLI
// supervisor (second tunnel + its own SIGINT handler on this process).
import { connect } from '../etunnel.ts';

const server = process.env.ETUNNEL_SERVER!;
const local = process.env.ETUNNEL_LOCAL || '3000';
const t = await connect(server, local);
console.log('APP_TUNNEL: ' + t.url);

process.on('SIGINT', async () => {
  await t.close();
  console.log('APP_CLOSED');
  process.exit(0);
});
