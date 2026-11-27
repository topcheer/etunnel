package etunnel

// Version is the Go SDK's own semver, advertised in hello.version so a release
// server can apply its client version policy (it refuses SDKs more than
// Config.ClientVersionLag patches behind). Stamped from VERSION by
// scripts/bump-release.py - keep it exactly "major.minor.patch", otherwise
// release servers would reject the SDK with client_outdated.
const Version = "1.7.52"
