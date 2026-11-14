# Multi-target routing demo

One public hostname serving two local services by URL path prefix - the
SPA prod-bundle shape (`/` to the built frontend, `/api` to the backend)
without CORS or a second tunnel.

```
go run ./examples/basic/multi -server wss://t.etun.uk/tunnel [-token et_...]
```

Starts two throwaway local echo services, opens ONE tunnel routed as
`/api -> backend (stripped)`, `/ -> frontend`, and prints what each
service received for three sample URLs. Contract: PROTOCOL.md
"Client-side multi-target routing".
