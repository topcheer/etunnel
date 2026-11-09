# etunnel for Java

[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)
[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)

Zero-dependency client built on `java.net.http.WebSocket` - works on JDK 11 and later, no Netty, no Jackson.

Every SDK speaks the same wire protocol against any etunnel server - the default is `wss://t.etun.uk/tunnel`, and `ETUNNEL_SERVER` or the first argument of `connect()` points at your own deployment.

```mermaid
flowchart LR
  V["visitor<br/>https://&lt;subdomain&gt;.<domain>"] -->|HTTPS| S["etunneld<br/>TLS + plan limits"]
  S -->|"wss /tunnel"| C["Java client<br/>in your process"]
  C -->|HTTP| A["localhost:3000"]
```

## Install

```sh
Maven:
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel</artifactId>
  <version>1.2.4</version>
</dependency>

Gradle:
implementation "dev.ggcode:etunnel:1.2.4"
```

## Embed in your app

```java
import dev.ggcode.Etunnel;

public class Main {
    public static void main(String[] args) throws Exception {
        Etunnel.Tunnel tunnel = Etunnel.connect("wss://t.etun.uk/tunnel", 3000, null);
        System.out.println("public URL: " + tunnel.url);

        Runtime.getRuntime().addShutdownHook(new Thread(tunnel::close));
        Thread.currentThread().join();   // keep the tunnel open
    }
}
```

## CLI

```sh
java src/main/java/dev/ggcode/Etunnel.java 3000   # single-file launch, JDK 11+
```

Prints `TUNNEL_URL: https://<subdomain>.<domain>`; `Ctrl-C` closes the tunnel and the server frees the subdomain immediately.

## API

| Symbol | Returns / effect |
|---|---|
| `Etunnel.connect(server, localPort, Options)` | `Tunnel` (blocking until `welcome`) |
| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | connection facts |
| `Tunnel.close()` | graceful shutdown |

Options: `new Etunnel.Options().token(...).subdomain(...)` - or pass `null` for an anonymous tunnel.

The tunnel keeps the URL for the lifetime of the process (or the plan TTL, whichever comes first); the server closes it with `bye` when the TTL expires, and `close()` releases it early.

Each account may hold as many concurrent tunnels as its plan allows (free 2, starter 5, pro 20); anonymous tunnels are additionally capped per source IP. Starting one too many is rejected with `limit_reached`.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | Tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | Local port or `host:port` (the first CLI argument wins) | `3000` |
| `ETUNNEL_TOKEN` | Paid-plan API key (enables custom subdomain + longer TTL) | - |
| `ETUNNEL_SUBDOMAIN` | Requested subdomain, paid plans only | random |

## Notes and limits

- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.
- One JVM thread per tunnel plus a small executor for stream handling; no global state.
- Behind a corporate proxy, `java.net.http` honours `-Dhttps.proxyHost` / `-Dhttps.proxyPort`.

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
