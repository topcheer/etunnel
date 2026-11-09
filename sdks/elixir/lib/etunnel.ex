defmodule Etunnel do
  # etunnel SDK for Elixir (websockex + finch + jason).
  # Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
  @moduledoc """
  Exposes a local service through an etunnel server.

      {:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
      Etunnel.url(t)
  """
  use WebSockex

  @maxp 32_768
  @hop ~w(connection transfer-encoding keep-alive upgrade proxy-connection te trailer content-length host)

  def start_link(opts) do
    server = Keyword.fetch!(opts, :server)
    local = normalize_local(Keyword.fetch!(opts, :local))
    token = opts[:token]
    subdomain = opts[:subdomain]

    state = %{local: local, token: token, subdomain: subdomain, url: nil, streams: %{}}

    WebSockex.start_link(server, __MODULE__, state,
      extra_headers: [{"Sec-WebSocket-Protocol", "etunnel-v1"}]
    )
  end

  defp normalize_local(local) when is_integer(local), do: "127.0.0.1:#{local}"
  defp normalize_local(local) when is_binary(local) do
    if Regex.match?(~r/^\d+$/, local), do: "127.0.0.1:#{local}", else: local
  end

  def url(pid), do: GenServer.call(pid, :url)

  def close(pid) do
    WebSockex.send_frame(pid, {:text, Jason.encode!(%{type: "bye", reason: "client closed"})})
  end

  @impl true
  def handle_connect(_conn, state) do
    send(self(), :etunnel_hello)
    {:ok, state}
  end

  @impl true
  def handle_frame({:text, msg}, state) do
    case Jason.decode(msg) do
      {:ok, %{"type" => "welcome"} = w} ->
        IO.puts("TUNNEL_URL: #{w["url"]}")
        {:ok, %{state | url: w["url"]}}

      {:ok, %{"type" => "error"} = e} ->
        IO.puts(:stderr, "etunnel: #{e["code"]}: #{e["message"]}")
        {:ok, state}

      {:ok, %{"type" => "req", "streamId" => sid} = req} ->
        {:ok, put_in(state, [:streams, sid], %{req: req, body: []})}

      {:ok, %{"type" => "ping"} = p} ->
        {:reply, {:text, Jason.encode!(%{type: "pong", ts: p["ts"] || 0})}, state}

      {:ok, %{"type" => "bye"}} ->
        {:ok, state}

      _ ->
        {:ok, state}
    end
  end

  def handle_frame({:binary, bin}, state) do
    case bin do
      <<kind, sid::big-32, n::big-32, rest::binary>> ->
        payload = binary_part(rest, 0, min(n, byte_size(rest)))

        case Map.fetch(state.streams, sid) do
          {:ok, st} ->
            case kind do
              1 -> {:ok, update_in(state, [:streams, sid, :body], &(&1 ++ [payload]))}
              2 ->
                state = update_in(state, [:streams], &Map.delete(&1, sid))
                body = IO.iodata_to_binary(st.body)
                me = self()
                Task.start(fn -> serve(me, sid, st.req, body, state.local) end)
                {:ok, state}
              3 ->
                {:ok, update_in(state, [:streams], &Map.delete(&1, sid))}
            end

          :error ->
            {:ok, state}
        end

      _ ->
        {:ok, state}
    end
  end

  @impl true
  def handle_call(:url, _from, state) do
    {:reply, state.url, state}
  end

  @impl true
  def handle_info(:etunnel_hello, state) do
    hello =
      %{type: "hello", proto: 1, client: "etunnel-elixir/1.0"}
      |> maybe_put(:subdomain, state.subdomain)
      |> maybe_put(:token, state.token)
      |> Jason.encode!()

    {:reply, {:text, hello}, state}
  end

  def handle_info({:etunnel_send, frame}, state) do
    {:reply, frame, state}
  end

  def handle_info(_msg, state), do: {:ok, state}

  defp maybe_put(map, _k, nil), do: map
  defp maybe_put(map, k, v), do: Map.put(map, k, v)

  # ---- request serving (runs in a Task; sends frames via the ws process) ----

  # Themed 502 page shown when the local app is down (same HTML as the other SDKs).
  defp local_error_page(msg) do
    esc =
      msg
      |> String.replace("&", "&amp;")
      |> String.replace("<", "&lt;")
      |> String.replace(">", "&gt;")

    """
    <!doctype html><html><head><meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>502 - local app down</title>
    <style>
    :root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
    display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
    main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
    .badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
    padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
    h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
    h1 .g{color:var(--green)}
    p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
    .panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
    .panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
    .panel .v{font-size:.9rem;word-break:break-all}
    .err{color:#f85149}
    .tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
    .tip b{color:var(--fg)}
    footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
    footer a{color:#58a6ff;text-decoration:none}
    .pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
    margin-right:.5em;animation:p 1.2s ease-in-out infinite}
    @keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
    @media(prefers-reduced-motion:reduce){.pulse{animation:none}}
    </style></head><body><main>
    <span class="badge">502 local app down</span>
    <h1>tunnel up, <span class="g">local app</span> is not</h1>
    <p>The tunnel connection is alive, but nothing answered when we knocked.</p>
    <div class="panel">
    <div class="k">error</div><div class="v err">#{esc}</div>
    </div>
    <div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
    the tunnel stays connected and keeps retrying for you.</div>
    <footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
    </main></body></html>
    """
  end

  defp serve(ws_pid, sid, req, body, local) do
    method = req["method"] || "GET"
    path = req["path"] || "/"

    headers =
      (req["headers"] || %{})
      |> Enum.reject(fn {k, _v} -> k in @hop end)
      |> Enum.flat_map(fn {k, vs} -> Enum.map(vs, &{String.to_charlist(k), String.to_charlist(&1)}) end)

    method_atom = method |> String.downcase() |> String.to_atom()
    url = String.to_charlist("http://#{local}#{path}")

    request =
      if method in ["GET", "HEAD"] do
        {url, headers}
      else
        {url, headers, 'application/octet-stream', body}
      end

    case :httpc.request(method_atom, request, [{:timeout, 120_000}], [{:body_format, :binary}, {:sync, true}]) do
      {:ok, {{_, status, _}, resp_headers, out}} ->
        hdrs =
          resp_headers
          |> Enum.reject(fn {k, _} -> to_string(k) |> String.downcase() |> then(&(&1 in @hop)) end)
          |> Enum.map(fn {k, v} -> {to_string(k), [to_string(v)]} end)
          |> Map.new()

        send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: status, headers: hdrs})}})
        send_chunks(ws_pid, sid, out || "", 0)
        send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})

      {:error, e} ->
        send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 502, headers: %{"Content-Type" => ["text/html; charset=utf-8"]}})}})
        send(ws_pid, {:etunnel_send, {:binary, frame(0x01, sid, local_error_page(inspect(e)))}})
        send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})
    end
  end

  defp send_chunks(_pid, _sid, out, i) when i >= byte_size(out), do: :ok

  defp send_chunks(pid, sid, out, i) do
    n = min(@maxp, byte_size(out) - i)
    chunk = binary_part(out, i, n)
    send(pid, {:etunnel_send, {:binary, frame(0x01, sid, chunk)}})
    send_chunks(pid, sid, out, i + n)
  end

  defp frame(kind, sid, payload) do
    <<kind, sid::big-32, byte_size(payload)::big-32, payload::binary>>
  end
end
