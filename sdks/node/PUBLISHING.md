# Publishing @easytunnel/et-sdk (OIDC trusted publishing)

This package publishes through npm **trusted publishing** (OIDC) from GitHub
Actions — no `NPM_TOKEN` secret anywhere. npm's trusted-publisher settings
live on the *package settings page*, which only exists **after the first
publish*, so bootstrap is a one-time manual step, then everything is CI-only.

## Bootstrap (one time, in order)

### 1. First publish — locally, manually

npm has no "pre-register a new package" flow; the package must exist before
its settings page (and thus the trusted-publisher form) appears. From this
directory:

```sh
npm login --registry https://registry.npmjs.org
npm publish --access public --provenance=false --registry https://registry.npmjs.org
```

The two flags are load-bearing:

- `--registry https://registry.npmjs.org` — a local machine often defaults to
  a mirror (e.g. npmmirror in CN); publishing must hit the real registry.
- `--provenance=false` — `package.json > publishConfig.provenance: true` is
  the CI/OIDC setting; locally there is no OIDC token, so npm would refuse.
  The CLI flag overrides publishConfig for this manual publish only.

The login account must be a member (owner) of the `easytunnel` npm org.

### 2. Bind the trusted publisher

Prerequisite on GitHub: repo `topcheer/etunnel` → Settings → Environments →
create an environment named exactly `npm` (the workflow's `publish-npm` job
declares `environment: npm`).

Then on npmjs.com, the package page now exists:
`@easytunnel/et-sdk` → **Settings → Publishing access → Trusted Publisher**,
and enter exactly:

| Field | Value |
|---|---|
| Repository | `topcheer/etunnel` |
| Workflow filename | `sdk-packages.yml` |
| Environment | `npm` |

### 3. Validate within the 48-hour window

A newly bound trusted publisher entry is provisional and **expires if no
successful publish happens within 48 hours** (then it must be deleted and
re-created). Validate immediately:

1. bump `version` to `1.0.1` in `package.json` (npm rejects re-publishing
   an existing version, so the manual `1.0.0` cannot be reused)
2. commit + push to `main`
3. `git tag v1.0.1 && git push origin v1.0.1`

The tag triggers `.github/workflows/sdk-packages.yml` → job `publish-npm`,
which publishes with OIDC provenance and flips the binding to permanent.

## Expected failure until bootstrap completes

Until step 2 (binding) is done, every tag-triggered run shows
`publish-npm` **failed** — the package either does not exist yet (step 1
pending) or has no trusted publisher bound (step 2 pending), and npm rejects
the OIDC identity with E401/E403/provenance errors. **That failure is
expected, not a bug.** Health signal is the two `pack` matrix jobs
(`pack (ubuntu-22.04)` and `pack (macos-14)`): if those are green, the
pipeline itself is sound and only the npm-side binding is missing.

## Day-2+ routine releases

After the binding is activated:

```sh
# bump version in sdks/node/package.json, then:
git commit -am "chore: node sdk vX.Y.Z" && git push
git tag vX.Y.Z && git push origin vX.Y.Z
```

Tag push → CI builds matrix → `publish-npm` publishes with provenance.
No tokens, no secrets, no manual npm step.

## Known issue (2026-11-08): OIDC push to an existing gem returns 403

`configure-rubygems-credentials@v2.1.0` has no `owner` input, and a manual
exchange (`POST /api/v1/oidc/trusted_publisher/exchange_token`, body
`{"jwt": ...}`) succeeds and returns a valid `push_rubygem` key, but
`gem push` of a version to the *already existing* gem still answers
"You are not allowed to push this gem." (pusher.rb: notify_unauthorized,
i.e. server thinks the key's owner doesn't own the gem). Same mechanics as
`release-gem@v1.4.1` (it shells the same credentials action + rake release).

Workaround until clarified with RubyGems.org: release gem versions manually
(`gem push` with a user API key; MFA via WebAuthn) - takes ~30s per release.
The workflow job stays `continue-on-error` so it never blocks the matrix.
