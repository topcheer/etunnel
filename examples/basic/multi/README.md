# Multi-target routing demo

One public hostname serving two local services by URL path prefix - the
SPA prod-bundle shape (`/` to the built frontend, `/api` to the backend)
without CORS or a second tunnel.

```
go run ./examples/basic/multi -server wss://t.etun.uk/tunnel [-token et_...]
```

`-server` is required for anything but the public default (the default is
`wss://t.etun.uk/tunnel`; this example does not read `ETUNNEL_SERVER`).

Starts two throwaway local echo services, opens ONE tunnel routed as
`/api -> backend (stripped)`, `/ -> frontend`, then requests three URLs
through the public hostname itself and prints what each service received:

```
public URL: http://quiet-heron-h9b6mz.localhost
GET /                    -> frontend saw /?
GET /app/page            -> frontend saw /app/page?
GET /api/users?x=1&y=2   -> api saw /users?x=1&y=2
```

(`/api` loses its prefix and keeps the query string; `/apiv2` would fall
through to the `/` route because matching is on segment boundaries.) The
output above is the real output against a local `-domain localhost` server;
on the public server the URL is `https://<name>.t.etun.uk`. The process exits
once the three requests are done - it is a demo, not a server.
Contract: PROTOCOL.md "Client-side multi-target routing".
