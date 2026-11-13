#!/bin/sh
# E2E: CLI loopback login (sdks/perl) against the shared Go loginserver fixture.
set -eu
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$REPO/sdks/perl"

(cd "$REPO" && go build -o /tmp/et-loginserver ./sdks/testtools/loginserver)
OUT=$(mktemp); /tmp/et-loginserver >"$OUT" 2>/dev/null &
FX=$!; trap 'kill $FX 2>/dev/null || true' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do grep -q listening "$OUT" && break; sleep 1; done
ADDR=$(grep listening "$OUT" | awk '{print $2}')
[ -n "$ADDR" ] || { echo "FAIL: fixture did not start"; exit 1; }
export FX_ADDR=$ADDR
cat > /tmp/perl_login_check.pl <<'PEOF'
do "./etunnel.pl" or die "load etunnel.pl: Inappropriate ioctl for device@{@}";
my $c = etunnel_login("http://" . $ENV{FX_ADDR}, sub {
    my ($u) = @_;
    system("curl -s -o /dev/null " . $u . " >/dev/null 2>&1 &") == 0;
}, 30);
print "CRED:" . $c->{api_token} . ":" . join(",", @{ $c->{subdomains} // [] }) . ":" . ($c->{plan} // "");
PEOF
RESULT=$(timeout 60 perl /tmp/perl_login_check.pl 2>&1) || RESULT="ERR:$RESULT"
case "$RESULT" in
  "CRED:tok-123:my-sub:pro") echo "PASS: loopback login start->grant->exchange" ;;
  *) echo "FAIL: $RESULT"; exit 1 ;;
esac
