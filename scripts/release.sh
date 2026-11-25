#!/usr/bin/env bash
# Hourly release driver (v1.7.41+ cadence, 2026-11-26).
#
#   scripts/release.sh            # next patch (VERSION + 1)
#   scripts/release.sh 1.7.42     # explicit version
#
# Does: bump all manifests -> changelog line -> commit -> tag -> push main+tag
# (as the topcheer account: JSR authenticates the triggering actor) -> GitHub
# Release. It does NOT wait for CI (publish-central alone waits ~30 min); check
# the run separately and `gh run rerun <id> --failed` when a registry hiccup
# (429) made an idempotency probe refuse to publish.
#
# Preconditions: clean tree on main, gh logged in as topcheer + ggaidev,
# go toolchain (make verify runs the full gate suite).
set -euo pipefail
cd "$(dirname "$0")/.."

CUR=$(cat VERSION)
if [ $# -ge 1 ]; then NEW=$1; else
  NEW=$(python3 - "$CUR" <<'EOF'
import sys
mj, mi, p = (int(x) for x in sys.argv[1].split("."))
print(f"{mj}.{mi}.{p + 1}")
EOF
)
fi

[ -z "$(git status --porcelain | grep -v '^??')" ] || { echo "release: tracked files dirty"; git status --porcelain | head -5; exit 1; }
[ "$(git branch --show-current)" = "main" ] || { echo "release: not on main"; exit 1; }
git pull -q --ff-only origin main

python3 scripts/bump-release.py "$NEW" >/dev/null
python3 - "$NEW" <<'EOF'
import sys
v = sys.argv[1]
p = "CHANGELOG.md"; s = open(p).read()
entry = f"## v{v} — 2026-11-26\n\n- release: hourly cadence (no code changes); SDKs report `hello.version` for the server-side client version gate (v1.7.40)\n\n"
assert s.startswith("# Changelog\n\n")
s = s.replace("# Changelog\n\n", "# Changelog\n\n" + entry, 1)
open(p, "w").write(s)
EOF
python3 scripts/bump-release.py --check | tail -1

git add -u
git commit -q -m "release: v$NEW

Hourly cadence; no code changes.

Co-Authored-By: ggcode <noreply@ggcode.dev>"
make verify > /tmp/release-verify-$NEW.log 2>&1 || { echo "release: make verify failed"; tail -20 /tmp/release-verify-$NEW.log; exit 1; }
git tag -a "v$NEW" -m "v$NEW"

# Push as topcheer: JSR trusts the tag's triggering actor (RUNBOOK §0.6).
gh auth switch -u topcheer >/dev/null
trap 'gh auth switch -u ggaidev >/dev/null 2>&1 || true' EXIT
git push -q origin main
git -c credential.https://github.com.username=topcheer push -q origin "v$NEW"

sleep 8
RUN=$(gh api "repos/topcheer/etunnel/actions/runs?event=push&per_page=5" --jq ".workflow_runs[] | select(.head_branch==\"v$NEW\") | .id" | head -1 || true)

git show origin/main:CHANGELOG.md | awk -v v="## v$NEW" '$0==v{f=1;next} /^## v/{if(f)exit} f' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' > "/tmp/rel-$NEW-notes.md"
gh release create "v$NEW" -R topcheer/etunnel --verify-tag --latest --title "v$NEW" --notes-file "/tmp/rel-$NEW-notes.md" >/dev/null

echo "released v$NEW (was v$CUR) commit=$(git rev-parse --short HEAD) run=${RUN:-unknown}"
