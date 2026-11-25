# shared helper sourced by each SDK's hello_version.sh
HV_REPO=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)
hv_build_fixture() {
  (cd "$HV_REPO" && go build -o /tmp/wssH ./sdks/testtools/wssserver) || { echo "FAIL: fixture build"; exit 1; }
}
# hv_run_case <log hello file|-> <error code|-> <sdk-run-command...>
hv_run_case() {
  LOGHELLO="$1"; ERR="$2"; shift 2
  # "-" disables the hook (otherwise the fixture would create a file named "-")
  [ "$LOGHELLO" = "-" ] && LOGHELLO=""
  [ "$ERR" = "-" ] && ERR=""
  rm -f /tmp/wssH.addr; [ -n "$LOGHELLO" ] && rm -f "$LOGHELLO"
  (cd "$HV_REPO" && ETUNNEL_FIXTURE_LOG_HELLO="$LOGHELLO" ETUNNEL_FIXTURE_ERROR="$ERR" /tmp/wssH -listen 127.0.0.1:0 -plain > /tmp/wssH.addr 2>/dev/null) &
  HV_FPID=$!
  for _ in $(seq 20); do [ -s /tmp/wssH.addr ] && break; sleep 0.3; done
  HV_ADDR=$(head -1 /tmp/wssH.addr)
}
hv_stop_fixture() { kill $HV_FPID 2>/dev/null || true; }
