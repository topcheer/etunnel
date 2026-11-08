local = hd(System.argv()) || System.get_env("ETUNNEL_LOCAL") || "3000"

{:ok, _t} =
  Etunnel.start_link(
    server: System.get_env("ETUNNEL_SERVER") || "wss://t.ggcode.uk/tunnel",
    local: local,
    token: System.get_env("ETUNNEL_TOKEN"),
    subdomain: System.get_env("ETUNNEL_SUBDOMAIN")
  )

Process.sleep(:infinity)
