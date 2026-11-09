# etunnel for Node.js

[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)
[![Node](https://img.shields.io/badge/node-%3E%3D22-brightgreen)](https://nodejs.org)

Expose a local service on the public internet from Node: one `await`, no dependencies (Node >= 22 ships a global `WebSocket`).

Every SDK speaks the same wire protocol against any etunnel server - the default is `wss://t.etun.uk/tunnel`, and `ETUNNEL_SERVER` or the first argument of `connect()` points at your own deployment.

```mermaid
flowchart LR
  V["visitor<br/>https://&lt;subdomain&gt;.<domain>"] -->|HTTPS| S["etunneld<br/>TLS + plan limits"]
  S -->|"wss /tunnel"| C["Node.js client<br/>in your process"]
  C -->|HTTP| A["localhost:3000"]
```

## Install

```sh
npm install @easytunnel/et-sdk
```

## Embed in your app

```js
const { connect } = require('@easytunnel/et-sdk');

const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
console.log('public URL:', tunnel.url);

// keep the process alive; close() sends a clean bye to the server
process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
```

## CLI

```sh
npx @easytunnel/et-sdk 3000
```

Prints `TUNNEL_URL: https://<subdomain>.<domain>`; `Ctrl-C` closes the tunnel and the server frees the subdomain immediately.

## API

| Symbol | Returns / effect |
|---|---|
| `connect(server, local, opts?)` | `Promise<Tunnel>` - resolves after the server's `welcome` frame |
| `Tunnel.url` | `string` - public HTTPS URL |
| `Tunnel.close(reason?)` | closes the socket, freeing the subdomain |

Options: `{ token, subdomain, client, onClose }` - `client` only overrides the user agent sent in `hello`.

The tunnel keeps the URL for the lifetime of the process (or the plan TTL, whichever comes first); the server closes it with `bye` when the TTL expires, and `close()` releases it early.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | Tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | Local port or `host:port` (the first CLI argument wins) | `3000` |
| `ETUNNEL_TOKEN` | Paid-plan API key (enables custom subdomain + longer TTL) | - |
| `ETUNNEL_SUBDOMAIN` | Requested subdomain, paid plans only | random |

## Notes and limits

- Buffer-based client: request/response bodies are buffered per stream. Fine for webhooks, dashboards and APIs; use the [Go SDK](../../sdk) for streaming or WebSocket upgrades.
- CommonJS and ESM both work (`import { connect } from '@easytunnel/et-sdk'`).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ws: cannot connect to ...` | DNS, corporate proxy or firewall blocks the outbound socket | `curl -sI https://t.etun.uk/` from the same host; set `HTTPS_PROXY` if you need one |
| Public URL answers `404 - no tunnel here` | The tunnel process exited, or the plan TTL expired | Restart the client; the subdomain is released on disconnect |
| `error: invalid_token` | `token` set but not a paid-plan API key | Copy the key from `/me` on the portal, or drop the token for an anonymous tunnel |
| `error: too_many_tunnels` | Per-IP anonymous tunnel cap on the server | Close old clients (`lsof -ti:PORT`), or use a paid plan with a token |
| `error: subdomain_taken` | Custom subdomain already claimed by another account | Pick another name, or remove it from the old account in `/me` |
| Request hangs, then `502` | Local service is not listening on the requested port | `curl -sv http://127.0.0.1:3000/` first |
| WebSocket endpoints fail through the tunnel | Buffer-based SDK (all but Go) cannot forward 101 upgrades | Use the [Go SDK](../../sdk) for WebSocket pass-through |

## See also

- [All 20 SDKs](../README.md) - install matrix and capability comparison
- [Wire protocol](../../PROTOCOL.md) - frames, control messages, stream lifecycle
- [Repository](https://github.com/topcheer/etunnel) - server, Go SDK, examples, self-hosting
- [LICENSE](../../LICENSE) - MIT
