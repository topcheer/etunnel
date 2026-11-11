#!/usr/bin/env python3
"""Single-source release versioning for etunnel.

- VERSION file is the one truth. `bump-release.py X.Y.Z` stamps EVERY sdk
  manifest (incl. lua rockspec rename) in one atomic pass with per-file
  verification, and fails loudly listing anything it could not update.
- `bump-release.py --check [X.Y.Z]` exits non-zero if any manifest disagrees
  (used by CI as a tag gate: tag vX.Y.Z requires all manifests == X.Y.Z).
"""
import json, pathlib, re, sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
VERSION_FILE = ROOT / "VERSION"

def ver() -> str:
    return VERSION_FILE.read_text().strip()

MANIFESTS = {
    "sdks/node/package.json":       ("json",  "version"),
    "sdks/typescript/package.json": ("json",  "version"),
    "sdks/deno/deno.json":          ("json",  "version"),
    "composer.json":                ("json",  "version"),
    "sdks/rust/Cargo.toml":         ("regex", r'(?m)^version = "[^"]*"'),
    "sdks/python/pyproject.toml":   ("regex", r'(?m)^version = "[^"]*"'),
    "sdks/dart/pubspec.yaml":       ("regex", r'(?m)^version: .*$'),
    "sdks/ruby/etunnel.gemspec":    ("regex", r"(?m)s\.version = '[^']*'"),
    "sdks/csharp/Etunnel.csproj":   ("regex", r"<Version>[^<]*</Version>"),
    "sdks/java/pom.xml":            ("nth",   ("<version>", 0)),
    "sdks/clojure/pom.xml":         ("nth",   ("<version>", 0)),
    "sdks/elixir/mix.exs":          ("regex", r'version: "[^"]*"'),
    "sdks/perl/Makefile.PL":        ("regex", r"(?m)VERSION\s+=> '[^']*'"),
    "sdks/lua/rockspec":            ("lua",   None),  # file discovered by glob
    "sdks/swift":                   ("tag",   None),  # SPM has no version field
    "sdks/c":                       ("tag",   None),
    "sdks/cpp":                     ("tag",   None),
    "sdks/kotlin":                  ("jar",   None),  # ships the java artifact
    "sdks/scala":                   ("jar",   None),
    "sdk (go module)":              ("tag",   None),  # versioned by git tags
}

def read_current(path: pathlib.Path, kind, spec):
    if kind == "json":
        return json.loads(path.read_text()).get("version")
    if kind == "regex":
        m = re.search(spec, path.read_text())
        if not m: return None
        return re.search(r"[\d]+\.[\d]+\.[\d]+", m.group(0)).group(0)
    if kind == "nth":
        tag, n = spec
        m = re.search(re.escape(tag) + r"([^<]*)", path.read_text())
        if not m: return None
        s = m.group(1).strip()
        return s if re.fullmatch(r"[\d.]+", s) else None
    return "tag"

def stamp(path: pathlib.Path, kind, spec, v):
    s = path.read_text()
    if kind == "json":
        d = json.loads(s); d["version"] = v
        path.write_text(json.dumps(d, indent=2) + "\n"); return True
    if kind == "regex":
        m = re.search(spec, s)
        if not m: return False
        new = re.sub(r"[\d]+\.[\d]+\.[\d]+", v, m.group(0), count=1)
        path.write_text(s[:m.start()] + new + s[m.end():]); return True
    if kind == "nth":
        tag, _ = spec
        m = re.search(re.escape(tag) + r"([^<]*)", s)
        if not m: return False
        path.write_text(s[:m.start()] + tag + v + s[m.end():]); return True
    return True

def bump(v):
    errors = []
    lua_done = False
    for name, (kind, spec) in MANIFESTS.items():
        if kind == "lua":
            rocks = sorted((ROOT / "sdks/lua").glob("*.rockspec"))
            if len(rocks) != 1:
                errors.append(f"lua: expected exactly 1 rockspec, found {[r.name for r in rocks]}")
                continue
            cur = rocks[0]
            body = cur.read_text()
            body = re.sub(r'(?m)^version = "[^"]*"', f'version = "{v}-1"', body, count=1)
            newp = cur.with_name(cur.name.replace(cur.name.split("-")[1], v))
            # ggcode.etunnel-<ver>-1.rockspec
            new_name = f"ggcode.etunnel-{v}-1.rockspec"
            newp = cur.with_name(new_name)
            newp.write_text(body)
            if newp != cur: cur.unlink()
            lua_done = True
            print(f"  lua: {new_name}")
            continue
        p = ROOT / name
        if kind in ("tag", "jar"):
            print(f"  {name}: {kind}-versioned (git tag / shared artifact)")
            continue
        if not p.exists():
            errors.append(f"{name}: file missing"); continue
        if not stamp(p, kind, spec, v):
            errors.append(f"{name}: pattern not found"); continue
        got = read_current(p, kind, spec)
        if got != v:
            errors.append(f"{name}: stamped but readback={got}"); continue
        print(f"  {name}: {v}")
    if not lua_done:
        errors.append("lua: rockspec not processed")
    if errors:
        print("FAILED entries:\n  " + "\n  ".join(errors)); sys.exit(1)
    VERSION_FILE.write_text(v + "\n")
    print(f"VERSION -> {v} ; all manifests verified in one pass")

def check(expect=None):
    v = expect or ver()
    bad = []
    for name, (kind, spec) in MANIFESTS.items():
        if kind in ("tag", "jar"):
            continue
        if kind == "lua":
            rocks = sorted((ROOT / "sdks/lua").glob("*.rockspec"))
            if len(rocks) != 1 or v not in rocks[0].name:
                bad.append(f"lua: {([r.name for r in rocks])} want *{v}*")
            continue
        p = ROOT / name
        if not p.exists():
            bad.append(f"{name}: missing"); continue
        got = read_current(p, kind, spec)
        if got != v:
            bad.append(f"{name}: {got} (want {v})")
    if bad:
        print("VERSION MISMATCH:\n  " + "\n  ".join(bad)); sys.exit(1)
    print(f"all versioned manifests == {v}")

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] in ("-h", "--help"):
        print(__doc__)
        print("usage: bump-release.py X.Y.Z   |  bump-release.py --check [X.Y.Z]")
        sys.exit(0 if len(sys.argv) >= 2 else 2)
    if sys.argv[1] == "--check":
        check(sys.argv[2] if len(sys.argv) > 2 else None)
    elif not re.fullmatch(r"v?[\d]+\.[\d]+\.[\d]+", sys.argv[1]):
        # guards against stamping arbitrary words (an early "--help" probe
        # corrupted every JSON manifest before this guard existed)
        print(f"refusing to stamp {sys.argv[1]!r}: not a semantic version"); sys.exit(2)
    else:
        bump(sys.argv[1].lstrip("v"))
