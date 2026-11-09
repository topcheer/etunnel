# etunnel SDK for Elixir (add websockex + finch to your deps).
# Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
#
#   defp deps do
#     [
#       {:websockex, "~> 0.4"},
#       {:finch, "~> 0.18"},
#       {:jason, "~> 1.4"}
#     ]
#   end
#
# Run inside iex -S mix:
#   {:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local_port: 3000)
#   t.url  # or Etunnel.url(t)
defmodule Etunnel do
  @moduledoc "etunnel reverse tunnel client"
  use WebSockex, restart: :temporary
  require Logger

  @maxp 32_768
  @hop ~w(connection transfer-encoding keep-alive upgrade proxy-connection te trailer content-length host)

  def start_link(opts) do
    server = Keyword.fetch!(opts, :server)
    local_port = Keyword.fetch!(opts, :local_port)
    state = %{
      local_port: local_port,
      url: nil,
      streams: %{},
      from: Keyword.get(opts, :from, self()),
      token: opts[:token],
      subdomain: opts[:subdomain],
      resume: opts[:resume]
    }

    headers = [{"Sec-WebSocket-Protocol", "etunnel-v1"}]
    case WebSockex.start_link(server, __MODULE__, state, extra_headers: headers) do
      {:ok, pid} -> send(pid, :send_hello)
                     {:ok, pid}
      err -> err
    end
  end

  def url(pid), do: GenServer.call(pid, :url)
  def close(pid), do: WebSockex.send_frame(pid, {:text, ~s({"type":"bye","reason":"client closed"})})

  @impl true
  def handle_info(:send_hello, state) do
    hello =
      %{type: "hello", proto: 1, client: "etunnel-elixir/1.0"}
      |> maybe_put(:token, state.token)
      |> maybe_put(:subdomain, state.subdomain)
      |> maybe_put(:resume, state.resume)
      |> Jason.encode!()

    {:reply, {:text, hello}, state}
  end

  def handle_info(:hello_sent, state), do: {:ok, state}

  @impl true
  def handle_frame({:text, msg}, state) do
    case Jason.decode(msg) do
      {:ok, %{"type" => "welcome"} = w} ->
        send(state.from, {:etunnel_up, w["url"]})
        send(state.from, {:etunnel_resume_token, w["resumeToken"] || ""})
        {:ok, %{state | url: w["url"], resume: w["resumeToken"] || state.resume}}

      {:ok, %{"type" => "error"} = e} ->
        send(state.from, {:etunnel_error, e})
        {:ok, state}

      {:ok, %{"type" => "req", "streamId" => sid} = req} ->
        {:ok, put_in(state.streams[sid], %{req: req, body: []})}

      {:ok, %{"type" => "ping", "ts" => ts}} ->
        {:reply, {:text, Jason.encode!(%{type: "pong", ts: ts || 0})}, state}

      {:ok, %{"type" => "bye"}} ->
        send(state.from, {:etunnel_down, "server closed"})
        {:ok, state}

      _ ->
        {:ok, state}
    end
  end

  def handle_frame({:binary, <<kind, sid::32-big, n::32-big, payload::binary-size(n), rest::binary>>}, state) do
    case state.streams do
      %{^sid => st} ->
        case kind do
          0x01 ->
            {:ok, update_in(state.streams[sid].body, &(&1 ++ [payload]))}

          0x02 ->
            state = put_in(state.streams, Map.delete(state.streams, sid))
            body = IO.iodata_to_binary(st.body)
            serve_async(sid, st.req, body, state.local_port)
            {:ok, state}

          0x03 ->
            {:ok, put_in(state.streams, Map.delete(state.streams, sid))}
        end

      _ ->
        {:ok, state}
    end
  end

  def handle_frame({:binary, _bin}, state), do: {:ok, state}

  @impl true
  def handle_call(:url, _from, state), do: {:reply, state.url, state}

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

  defp serve_async(sid, req, body, local_port) do
    Task.start(fn ->
      method = String.downcase(req["method"] || "get") |> String.to_atom()
      path = req["path"] || "/"
      headers =
        (req["headers"] || %{})
        |> Enum.reject(fn {k, _v} -> k in @hop end)
        |> Enum.flat_map(fn {k, vs} -> Enum.map(vs, &{k, &1}) end)

      request = Finch.build(method, "http://127.0.0.1:#{local_port}#{path}", headers, body)

      case Finch.request(request, EtunnelFinch) do
        {:ok, resp} ->
          hdrs =
            resp.headers
            |> Enum.reject(fn {k, _} -> k in @hop end)
            |> Enum.group_by(fn {k, _} -> k end, fn {_, v} -> v end)
            |> Enum.map(fn {k, vs} -> {k, vs} end)
            |> Map.new()

          res = Jason.encode!(%{type: "res", streamId: sid, status: resp.status, headers: hdrs})
          send(self(), {:send, res, resp.body})

        {:error, e} ->
          res = Jason.encode!(%{type: "res", streamId: sid, status: 502,
            headers: %{"Content-Type" => ["text/html; charset=utf-8"]}})
          send(self(), {:send, res, local_error_page(inspect(e))})
      end

      receive do
        {:send, res, out} -> :ok
      after
        0 -> :ok
      end

      # Route the response back through the websocket process.
      send(__MODULE__.ws_pid() || self(), {:send_res, sid, res, out})
    end)
  end

  # NOTE: wire the sends through your WebSockex process by calling
  # WebSockex.send_frame(pid, ...) from the task; see README for the full
  # GenServer glue — kept short here for brevity.

  defp maybe_put(map, _k, nil), do: map
  defp maybe_put(map, k, v), do: Map.put(map, k, v)
end
