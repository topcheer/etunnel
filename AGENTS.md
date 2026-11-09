# AGENTS.md

Guidance for coding agents working in this repository. Durable conventions
only — verify against the code before relying on any specific detail.

## Project Snapshot

- **etunnel** — a reverse-tunnel service (like localhost.run / Cloudflare
  Tunnel) with zero registration: an SDK client connects out over one
  WebSocket, gets a random subdomain with automatic TLS, and the internet can
  reach the developer's local service at `https://<subdomain>.<domain>`.
- Go module: `github.com/topcheer/etunnel` (single module; Go >= 1.26).
- Live deployment: `wss://t.etun.uk/tunnel`, tunnels at
  `https://<name>.t.etun.uk`.
- Deliberately small dependency set: `coder/websocket` (WS transport),
  `go-acme/lego/v4` (ACME TLS incl. DNS-01), `golang.org/x/crypto`,
  `golang.org/x/time` (rate limiting). Do not add dependencies casually.
- Product surface: free anonymous tier + paid tiers (custom subdomains,
  longer TTL, bandwidth caps), OAuth login (Google/GitHub/GitLab/Apple),
  payments (PayPal/Stripe/NOWPayments), Telegram bot for notifications.

## Validation

Commands as actually used by Makefile and CI
(`.github/workflows/sdk-packages.yml`):

- Build: `go build ./...`
- Test: `go test -count=1 ./...` (CI's "Verify core" step; `-count=1` defeats
  the test cache deliberately)
- Vet/format (local only, not in CI): `go vet ./...`, `gofmt -w .`
- Everything smoke-checked: `make verify` — version-consistency check, Go
  build+test, plus syntax/compile checks for several language SDKs
- Package all SDK artifacts into `dist/packages/`: `make pack-all`
- Version gate (CI requires this on `v*` tags):
  `python3 scripts/bump-release.py --check [X.Y.Z]`
- CI publishes to registries (npm/PyPI/RubyGems/... via OIDC trusted
  publishing) only on `v*` tags; it runs on ubuntu-22.04 and installs all
  language toolchains there.

## Architecture

- `protocol/` — the wire protocol shared by server and every SDK.
  - `frame.go`: binary stream frames (9-byte header: kind byte, uint32
    stream ID, uint32 length; DATA/FIN/RST; `MaxPayload` = 32 KiB).
  - `message.go`: JSON text control messages
    (hello/welcome/req/res/ping/pong/bye/error).
  - `name.go`: random `word-word-NN` subdomains + normalization.
  - `PROTOCOL.md` (repo root) is the normative spec — update it whenever the
    wire protocol changes.
- `sdk/` — the canonical Go SDK (`Connect`, `Options`, `Tunnel`,
  `stream.go`). It is package path `etunnel/sdk` inside this module;
  `sdk.DefaultServer` is overridable via `-ldflags -X`.
- `server/` — the `etunneld` server: `session.go` (one WS connection per
  tunnel, multiplexed streams, TTL expiry, token-bucket bandwidth),
  `proxy.go` (HTTP reverse proxy by hostname + WebSocket pass-through,
  landing page + branded 404), `registry.go` (hostname → session),
  `auth.go` (API keys + OAuth), `payments.go`, `portal.go` (user portal:
  `/login` + `/me` actions), `account.go` (JSON account
  store), `admin.go` (stats/admin API), `telegram.go`, `tls.go` (ondemand /
  dns / static TLS modes), `acme_dns.go` (DNS-01 via lego), `config.go`.
- `cmd/etunnel/` — client CLI (`etunnel -server ... 3000`).
- `cmd/etunneld/` — server daemon entrypoint.
- `sdks/` — 19 additional single-file, dependency-light language SDKs
  (Node, TS, Deno, Python, Java, Kotlin, C#, Rust, Swift, Dart, Ruby, PHP,
  Perl, Lua, Scala, Elixir, C, C++, Clojure). Together with `sdk/` these are
  "the 20 SDKs". See `sdks/README.md` and `PACKAGING.md`.
- `scripts/` — release/versioning tooling (`bump-release.py`) and README
  generators.
- Root `integration_test.go`, `upgrade_passthrough_test.go` — black-box
  end-to-end tests (package `etunnel_test`) spinning up a real server +
  tunnel via `httptest`.

## Coding Conventions

- **Go style**: gofmt (tabs); doc comments on every exported symbol and a
  package comment; idiomatic error handling — sentinel `errors.New` values
  and typed errors (e.g. `APIError{Code, Message}`); error strings are
  prefixed `etunnel: `; `context.Context` is the first parameter; shared
  state guarded by `sync.Mutex` / atomics, teardown via `sync.Once`.
- **Logging**: the Go SDK has no logging dependency — it exposes an optional
  `OnLog func(format string, args ...any)` callback. Server/CLI use the
  standard `log` package. Do not introduce logging frameworks.
- **Tests**: colocated `*_test.go` per package; table-driven where natural;
  integration tests live at the repo root in `etunnel_test` using the
  `startTestServer(t, mut)` helper (`t.Helper` + `t.Cleanup` everywhere).
  Tests must pass without network access and without DNS/ACME.
- **Cross-SDK parity contract**: every language SDK, standalone, honors the
  env vars `ETUNNEL_SERVER` / `ETUNNEL_LOCAL` / `ETUNNEL_TOKEN` /
  `ETUNNEL_SUBDOMAIN` and prints `TUNNEL_URL: <url>` on connect. New client
  features must be mirrored across all 20 SDKs — a protocol/behavior change
  that only lands in `sdk/` is incomplete.
- **Wire protocol robustness**: the server sniffs the first byte of text
  frames to tolerate SDKs that send binary frames with the text opcode;
  bodies larger than 32 KiB are chunked across DATA frames. Keep old clients
  working when evolving the protocol; bump `proto` and keep back-compat.
- **Versioning / release**: `VERSION` is the single source of truth. Never
  hand-edit per-SDK manifests — run `python3 scripts/bump-release.py X.Y.Z`
  which stamps every manifest atomically (CI's tag gate enforces this).
  Release = bump, commit, `git tag vX.Y.Z`, push tag; CI builds and
  publishes. See `PACKAGING.md` for registry specifics.
- **Docs**: `README.md` (product), `PROTOCOL.md` (wire spec), `PACKAGING.md`
  (registries), `sdks/README.md` (per-SDK usage). Keep them in sync with
  behavior changes; `scripts/gen-readmes.py` regenerates SDK READMEs.
