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
    # #337-4 (LOW): keep the hello client strings and Cargo.lock in the stamp
    # chain - they used to drift (etunnel-ts/1.6.11 vs VERSION 1.7.29,
    # Cargo.lock stuck at 1.7.28).
    "sdks/node/etunnel.js":         ("regex",  r"(?m)etunnel-node/[0-9]+\.[0-9]+\.[0-9]+"),
    "sdks/typescript/etunnel.ts":   ("regex",  r"(?m)etunnel-ts/[0-9]+\.[0-9]+\.[0-9]+"),
    "sdks/deno/etunnel.ts":         ("regex",  r"(?m)etunnel-deno/[0-9]+\.[0-9]+\.[0-9]+"),
    "sdks/rust/Cargo.lock":         ("regex",  r'(?m)name = "etunnel"\nversion = "[^"]*"'),
    "sdks/python/pyproject.toml":   ("regex", r'(?m)^version = "[^"]*"'),
    # #419: hello.client version literal must track the package version.
    "sdks/python/etunnel.py":       ("regex", r'(?m)^__version__ = "[^"]*"'),
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
    "sdks/android/build.gradle.kts": ("gradle", None),  # see ANDROID_* patterns below
    # oh-package.json5 is JSON5: a regex stamp leaves the file's formatting
    # (and any JSON5 syntax) intact instead of json.dumps reformatting it.
    "sdks/harmonyos/oh-package.json5": ("regex", r'(?m)^\s*"version":\s*"[^"]*"'),
    # Kotlin/Scala on Maven Central (feature: publish-kotlin-scala): the poms are
    # the manifests now (dev.ggcode:etunnel-kotlin / etunnel_2.13 / etunnel_3).
    "sdks/kotlin/pom.xml":         ("nth",   ("<version>", 0)),
    "sdks/scala/pom-2.13.xml":     ("nth",   ("<version>", 0)),
    "sdks/scala/pom-3.xml":        ("nth",   ("<version>", 0)),
    "sdk (go module)":              ("tag",   None),  # versioned by git tags
}

# Android AAR (#260): the release version lives in two places that must
# agree - the Gradle project version line and the vanniktech coordinates()
# literal. CI's sdk-packages publish-android job greps coordinates() for the
# version it idempotency-checks against Maven Central, so both are stamped
# in one pass; --check fails if either drifts.
ANDROID_VERSION_LINE = r'(?m)^version = "[^"]*"'
ANDROID_COORDS_LINE = r'(?m)coordinates\("dev\.ggcode", "etunnel-android", "[^"]*"\)'

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
    if kind == "gradle":
        s = path.read_text()
        got = []
        for pat in (ANDROID_VERSION_LINE, ANDROID_COORDS_LINE):
            m = re.search(pat, s)
            if not m: return None
            got.append(re.search(r"[\d]+\.[\d]+\.[\d]+", m.group(0)).group(0))
        return got[0] if got[0] == got[1] else None
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
    if kind == "gradle":
        for pat in (ANDROID_VERSION_LINE, ANDROID_COORDS_LINE):
            m = re.search(pat, s)
            if not m: return False
            new = re.sub(r"[\d]+\.[\d]+\.[\d]+", v, m.group(0), count=1)
            s = s[:m.start()] + new + s[m.end():]
        path.write_text(s); return True
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
            # #417 E28: the rockspec pins source.tag (git+https source). Rename-only
            # stamping shipped v1.7.35 with tag = "v1.4.0" -> `luarocks install`
            # fetched a tree without etunnel.lua at the expected path. Stamp it.
            stamped = re.sub(r'(?m)^(\s*)tag = "v[^"]*"', rf'\1tag = "v{v}"', body, count=1)
            if stamped == body and f'tag = "v{v}"' not in body:
                errors.append(f"lua: {cur.name} has no source.tag to stamp")
            body = stamped
            # ggcode.etunnel-<ver>-1.rockspec
            new_name = f"ggcode.etunnel-{v}-1.rockspec"
            newp = cur.with_name(new_name)
            newp.write_text(body)
            if newp != cur:
                cur.unlink()
                # Stage the rename ourselves: a plain `git add -u` (or `-A`
                # from a release script) only records the deletion of the
                # old path; the new file stays untracked and CI checks out
                # a tree with zero rockspecs (v1.6.10 burned a version on
                # exactly this). Self-contained beats docs.
                import subprocess
                r = subprocess.run(["git", "add", "--", str(newp), str(cur)], cwd=ROOT)
                if r.returncode != 0:
                    errors.append(f"lua: rockspec staged failed (git add)")
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
    append_changelog(v)
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
            elif not re.search(r'(?m)^\s*tag = "v' + re.escape(v) + r'"', rocks[0].read_text()):
                bad.append(f"lua: {rocks[0].name} source.tag != v{v} (rock would fetch a stale tree)")
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

def append_changelog(version: str) -> None:
    """Insert a skeleton '## vX.Y.Z — date' entry so the log never goes stale (#168).

    Best-effort: if CHANGELOG.md is missing or git is unavailable, bumping
    still proceeds (releases must not block on docs); CI or the author fills
    the body in a follow-up.
    """
    import datetime, re, subprocess
    path = ROOT / "CHANGELOG.md"
    if not path.exists():
        return
    try:
        with open(path, "r", encoding="utf-8") as f:
            content = f.read()
    except OSError:
        return
    if f"## v{version}" in content:
        return  # already recorded
    try:
        date = (
            subprocess.run(
                ["git", "log", "-1", "--format=%as"], cwd=ROOT,
                capture_output=True, text=True, check=True,
            ).stdout.strip()
            or datetime.date.today().isoformat()
        )
    except Exception:
        date = datetime.date.today().isoformat()
    entry = f"## v{version} — {date}\n\n- (fill in)\n\n"
    content = re.sub(r"(# Changelog\n\n)", r"\1" + entry, content, count=1)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)


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
