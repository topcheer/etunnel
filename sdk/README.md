# etunnel SDK for Go (canonical)

The reference SDK: full **streaming** bodies and **WebSocket (101 upgrade)
pass-through** — everything the buffer-based ports in `../sdks/` build down to.

```go
import "github.com/topcheer/etunnel/sdk"

t, err := sdk.Connect("wss://t.etun.uk/tunnel", "127.0.0.1:3000",
    sdk.Options{Token: os.Getenv("ETUNNEL_TOKEN")})
// t.URL() -> https://<subdomain>.t.etun.uk
defer t.Close()
```

- Zero dependencies outside the Go standard library.
- Manifest: the module's root `go.mod` (`module etunnel`) — installed via
  `go get github.com/topcheer/etunnel@v1.0.2` once the repo is tagged, served by proxy.golang.org.
- Distributed through the Go module proxy (git tags); no package upload step.
- Race-detector-enabled integration tests live at the repo root
  (`integration_test.go`).

See [../PROTOCOL.md](../PROTOCOL.md) for the wire protocol and
[../sdks/README.md](../sdks/README.md) for the other 19 languages.
