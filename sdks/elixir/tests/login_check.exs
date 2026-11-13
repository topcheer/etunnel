# E2E harness: run by tests/test_login.sh against the shared Go loginserver
# fixture (FX_ADDR env). Prints CRED:token:subs:plan on success.
creds = Etunnel.login("http://" <> System.get_env("FX_ADDR"),
  fn url ->
    System.cmd("sh", ["-c", "curl -s -o /dev/null '#{url}' >/dev/null 2>&1 &"], stderr_to_stdout: true)
    true
  end,
  30)
IO.puts("CRED:#{creds.api_token}:#{Enum.join(creds.subdomains, ",")}:#{creds.plan}")
