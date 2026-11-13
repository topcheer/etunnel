// E2E harness: run by tests/test_login.sh (node >= 23 strip-types) against
// the shared Go loginserver fixture (FX_ADDR env).
import { login } from "../etunnel.ts";
import { execFile } from "node:child_process";

const c = await login("http://" + process.env["FX_ADDR"]!, {
  openBrowser: (u) => {
    execFile("curl", ["-s", "-o", "/dev/null", u], () => {});
  },
  timeoutMs: 30000,
});
console.log(`CRED:${c.api_token}:${c.subdomains?.join(":") ?? ""}:${c.plan}`);
