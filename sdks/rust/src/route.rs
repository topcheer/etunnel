// Multi-target local routing (mirrors sdk/route.go semantics exactly) plus
// the CLI config-file parser. Zero extra dependencies: the rules are small
// and pinned by tests on both sides.

#[derive(Clone, Debug, PartialEq)]
pub struct Route {
    pub prefix: String, // public path prefix, "/" = catch-all
    pub addr: String,   // local service address
    pub strip: bool,    // remove the matched prefix (query preserved)
}

/// Validate + compile a route table: trims trailing slashes ("/" stays),
/// rejects duplicates and malformed prefixes, sorts longest-first so
/// matching needs no special cases.
pub fn normalize(routes: &mut Vec<Route>) -> Result<(), String> {
    for r in routes.iter_mut() {
        if r.addr.is_empty() {
            return Err(format!("etunnel: route: Addr is required"));
        }
        if r.prefix.is_empty() {
            return Err(format!("etunnel: route: PathPrefix is required (use \"/\" for the fallback route)"));
        }
        if !r.prefix.starts_with('/') {
            return Err(format!("etunnel: route: PathPrefix {:?} must start with '/'", r.prefix));
        }
        while r.prefix.len() > 1 && r.prefix.ends_with('/') {
            r.prefix.pop();
        }
    }
    let mut seen: Vec<&str> = Vec::new();
    for r in routes.iter() {
        if seen.contains(&r.prefix.as_str()) {
            return Err(format!("etunnel: route: duplicate PathPrefix {:?}", r.prefix));
        }
        seen.push(&r.prefix);
    }
    routes.sort_by(|a, b| b.prefix.len().cmp(&a.prefix.len()));
    Ok(())
}

/// Resolve a request "path?query" against the table. None = no match
/// (caller answers 404). The query string is carried over byte-for-byte;
/// only the path part is matched and (optionally) stripped.
pub fn route_for(routes: &[Route], path_and_query: &str) -> Option<(String, String)> {
    let (path, query) = match path_and_query.find('?') {
        Some(i) => (&path_and_query[..i], &path_and_query[i..]),
        None => (path_and_query, ""),
    };
    for r in routes {
        if !segment_match(path, &r.prefix) {
            continue;
        }
        let mut out = if r.strip { strip(path, &r.prefix) } else { path.to_string() };
        if !query.is_empty() {
            out.push_str(query);
        }
        return Some((r.addr.clone(), out));
    }
    None
}

/// "/api" matches "/api" and "/api/..." but never "/apiv2".
fn segment_match(path: &str, prefix: &str) -> bool {
    if prefix == "/" {
        return true;
    }
    path == prefix || path.starts_with(&format!("{}/", prefix))
}

/// Strip a matched prefix; exact match bottoms out at "/"; stripping the
/// root prefix is a no-op (the path is already rooted).
fn strip(path: &str, prefix: &str) -> String {
    if prefix == "/" {
        return path.to_string();
    }
    if path == prefix {
        return "/".into();
    }
    path[prefix.len()..].to_string()
}

// --- CLI config file (etunnel.conf) --------------------------------------
//
// Plain key = value lines; `[route]` starts one route block. Comments (#)
// and blank lines are ignored. Values are taken verbatim (no quotes, no
// escaping): addresses and prefixes never need them.
//
//   server = wss://t.etun.uk/tunnel
//   token = et_...
//   subdomain = myapp
//   [route]
//   prefix = /api
//   addr = 127.0.0.1:8080
//   strip = true
//   [route]
//   prefix = /
//   addr = 3000        # bare ports become 127.0.0.1:3000

#[derive(Default, Debug, PartialEq)]
pub struct Conf {
    pub server: Option<String>,
    pub token: Option<String>,
    pub subdomain: Option<String>,
    pub routes: Vec<Route>,
}

pub fn parse_conf(text: &str) -> Result<Conf, String> {
    let mut conf = Conf::default();
    let mut in_route = false;
    for (n, raw) in text.lines().enumerate() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line == "[route]" {
            conf.routes.push(Route { prefix: String::new(), addr: String::new(), strip: false });
            in_route = true;
            continue;
        }
        let (k, v) = match line.split_once('=') {
            Some((k, v)) => (k.trim(), v.trim()),
            None => return Err(format!("etunnel: config line {}: want key = value, got {:?}", n + 1, line)),
        };
        let v = v.split('#').next().unwrap_or("").trim(); // trailing comments
        let slot: &mut String = if in_route {
            let last = conf.routes.last_mut().unwrap();
            match k {
                "prefix" => &mut last.prefix,
                "addr" => &mut last.addr,
                "strip" => {
                    last.strip = v == "true" || v == "1";
                    if !last.strip && !(v == "false" || v == "0" || v.is_empty()) {
                        return Err(format!("etunnel: config line {}: strip must be true/false", n + 1));
                    }
                    continue;
                }
                _ => return Err(format!("etunnel: config line {}: unknown route key {:?}", n + 1, k)),
            }
        } else {
            match k {
                "server" => conf.server.get_or_insert_with(String::new),
                "token" => conf.token.get_or_insert_with(String::new),
                "subdomain" => conf.subdomain.get_or_insert_with(String::new),
                _ => return Err(format!("etunnel: config line {}: unknown key {:?}", n + 1, k)),
            }
        };
        *slot = v.to_string();
    }
    for r in conf.routes.iter_mut() {
        if r.addr.split('#').next().unwrap_or("").trim().parse::<u16>().is_ok() {
            r.addr = format!("127.0.0.1:{}", r.addr);
        } else if r.addr.starts_with(':') {
            r.addr = format!("127.0.0.1{}", r.addr);
        }
    }
    normalize(&mut conf.routes)?;
    Ok(conf)
}

/// Parse one `PREFIX=ADDR[,strip]` CLI spec (bare ports get 127.0.0.1).
pub fn parse_route_spec(spec: &str) -> Result<Route, String> {
    let (prefix, rest) = spec
        .split_once('=')
        .ok_or_else(|| format!("etunnel: route {:?}: want PREFIX=ADDR[,strip]", spec))?;
    let (addr, strip) = match rest.strip_suffix(",strip") {
        Some(a) => (a, true),
        None => (rest, false),
    };
    if addr.is_empty() {
        return Err(format!("etunnel: route {:?}: empty address", spec));
    }
    let addr = if addr.parse::<u16>().is_ok() {
        format!("127.0.0.1:{}", addr)
    } else if addr.starts_with(':') {
        format!("127.0.0.1{}", addr)
    } else {
        addr.to_string()
    };
    Ok(Route { prefix: prefix.to_string(), addr, strip })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn table() -> Vec<Route> {
        let mut rs = vec![
            Route { prefix: "/".into(), addr: "fe:1".into(), strip: false },
            Route { prefix: "/api".into(), addr: "be:1".into(), strip: false },
            Route { prefix: "/api/v2".into(), addr: "be2:1".into(), strip: false },
        ];
        normalize(&mut rs).unwrap();
        rs
    }

    #[test]
    fn segment_boundaries_and_longest_prefix() {
        let rs = table();
        for (path, want) in [
            ("/", "fe:1"),
            ("/index.html", "fe:1"),
            ("/apiv2/x", "fe:1"),   // boundary: /api must not match
            ("/apiary", "fe:1"),
            ("/api", "be:1"),
            ("/api/", "be:1"),
            ("/api/users", "be:1"),
            ("/api/v2", "be2:1"), // longest wins
            ("/api/v2/x?y=1", "be2:1"),
        ] {
            let (addr, _) = route_for(&rs, path).unwrap_or_else(|| panic!("no match: {path}"));
            assert_eq!(addr, want, "{path}");
        }
    }

    #[test]
    fn strip_preserves_query_and_bottoms_at_root() {
        let mut rs = vec![
            Route { prefix: "/api".into(), addr: "be:1".into(), strip: true },
            Route { prefix: "/".into(), addr: "fe:1".into(), strip: true }, // root strip = no-op
        ];
        normalize(&mut rs).unwrap();
        for (input, want_path) in [
            ("/api/users?x=1&y=2", "/users?x=1&y=2"),
            ("/api", "/"),
            ("/api/", "/"),
            ("/app?cfg=1", "/app?cfg=1"),
            ("/?q", "/?q"),
        ] {
            let (_, got) = route_for(&rs, input).unwrap();
            assert_eq!(got, want_path, "{input}");
        }
    }

    #[test]
    fn passthrough_without_strip() {
        let rs = table();
        for p in ["/api", "/api/users?x=1"] {
            let (addr, got) = route_for(&rs, p).unwrap();
            assert_eq!((addr.as_str(), got.as_str()), ("be:1", p));
        }
    }

    #[test]
    fn no_match_is_none() {
        let mut rs = vec![Route { prefix: "/api".into(), addr: "be:1".into(), strip: false }];
        normalize(&mut rs).unwrap();
        assert!(route_for(&rs, "/other").is_none());
        assert!(route_for(&rs, "/api/x").is_some());
    }

    #[test]
    fn normalize_rejects_bad_tables() {
        let mut dup = vec![
            Route { prefix: "/api".into(), addr: "a:1".into(), strip: false },
            Route { prefix: "/api/".into(), addr: "a:2".into(), strip: false },
        ];
        assert!(normalize(&mut dup).is_err());
        let mut noaddr = vec![Route { prefix: "/".into(), addr: String::new(), strip: false }];
        assert!(normalize(&mut noaddr).is_err());
        let mut noslash = vec![Route { prefix: "api".into(), addr: "a:1".into(), strip: false }];
        assert!(normalize(&mut noslash).is_err());
    }

    #[test]
    fn conf_roundtrip() {
        let c = parse_conf(
            "# demo\nserver = wss://t.etun.uk/tunnel\ntoken = et_x\n\n[route]\nprefix = /api\naddr = 8080 # trailing comment\nstrip = true\n[route]\nprefix = /\naddr = 3000\n",
        )
        .unwrap();
        assert_eq!(c.server.as_deref(), Some("wss://t.etun.uk/tunnel"));
        assert_eq!(c.routes.len(), 2);
        assert_eq!(c.routes[0].prefix, "/api");
        assert_eq!(c.routes[0].addr, "127.0.0.1:8080");
        assert!(c.routes[0].strip);
        assert_eq!(c.routes[1].addr, "127.0.0.1:3000");
    }

    #[test]
    fn conf_rejects_garbage() {
        assert!(parse_conf("nope\n").is_err());
        assert!(parse_conf("unknown = 1\n").is_err());
        assert!(parse_conf("[route]\nnope = 1\n").is_err());
        assert!(parse_conf("[route]\nprefix = /\n").is_err()); // empty addr
    }

    #[test]
    fn cli_spec_parsing() {
        let r = parse_route_spec("/api=127.0.0.1:8080,strip").unwrap();
        assert_eq!(r, Route { prefix: "/api".into(), addr: "127.0.0.1:8080".into(), strip: true });
        assert_eq!(parse_route_spec("/=3000").unwrap().addr, "127.0.0.1:3000");
        assert_eq!(parse_route_spec("/ws=:9001").unwrap().addr, "127.0.0.1:9001");
        assert!(parse_route_spec("/api").is_err());
        assert!(parse_route_spec("/api=").is_err());
    }
}
