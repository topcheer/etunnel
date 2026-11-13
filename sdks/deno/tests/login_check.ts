// E2E harness: run by tests/test_login.sh against the shared Go loginserver
// fixture (FX_ADDR env). Prints CRED:token:subs:plan on success.
const { login } = await import("../etunnel.ts");
const c = await login("http://" + Deno.env.get("FX_ADDR")!, {
  openBrowser: (u) => {
    new Deno.Command("curl", { args: ["-s", "-o", "/dev/null", u], stdout: "null", stderr: "null" }).spawn();
  },
  timeoutMs: 30000,
});
console.log(`CRED:${c.api_token}:${c.subdomains.join(":")}:${c.plan}`);
