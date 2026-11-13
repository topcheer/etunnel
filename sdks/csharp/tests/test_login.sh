#!/bin/sh
# E2E: CLI loopback login (sdks/csharp) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/csharp"
(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }
export FX_ADDR=$ADDR
mkdir -p /tmp/cs_login
# Etunnel.cs's CLI supervisor now lives in an explicit Cli.Main (not top-level
# statements), so the file can be compiled as a library into this mini exe.
cp Etunnel.cs /tmp/cs_login/
cat > /tmp/cs_login/LoginCheck.csproj <<'CEOF'
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net10.0</TargetFramework><Nullable>enable</Nullable><ImplicitUsings>enable</ImplicitUsings><StartupObject>LoginCheckMain</StartupObject></PropertyGroup>
</Project>
CEOF
cat > /tmp/cs_login/Program.cs <<'PEOF'
public static class LoginCheckMain {
    public static async Task Main() {
        var c = await EtunnelLogin.LoginAsync("http://" + Environment.GetEnvironmentVariable("FX_ADDR"),
            async u => { _ = System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo("curl", $"-s -o /dev/null {u}")); await Task.CompletedTask; });
        Console.WriteLine($"CRED:{c.ApiToken}:{string.Join(",", c.Subdomains)}:{c.Plan}");
    }
}
PEOF
RESULT=$(timeout 120 dotnet run --project /tmp/cs_login/LoginCheck.csproj 2>&1 | tail -1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
