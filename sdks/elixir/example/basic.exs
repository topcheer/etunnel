# Minimal etunnel example (Elixir). Add :etunnel (path or hex) to deps, then:
#   iex -S mix run example/basic.exs
{:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
IO.puts("public URL: " <> Etunnel.url(t))
Process.sleep(:infinity)
