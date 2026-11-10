defmodule Etunnel do
  # etunnel SDK for Elixir (websockex + jason).
  # Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
  @moduledoc """
  Exposes a local service through an etunnel server.

      {:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
      Etunnel.url(t)

  TLS is verified against the OS CA store by default (`verify: :verify_peer`).
  Pass `tls_verify: :verify_none` only for throwaway self-signed servers.

  On every welcome the server hands back a signed `resumeToken`; it is kept in
  the process state and delivered to the `start_link` caller as
  `{:etunnel_resume_token, token}`. Replay it via `resume: token` on the next
  `start_link` to reclaim the same subdomain after a disconnect.
  """
  use WebSockex

  @maxp 32_768
  @hop ~w(connection transfer-encoding keep-alive upgrade proxy-connection te trailer content-length host)
  @url_timeout 10_000

  # Fixed atom set: the server-controlled method string must never reach
  # String.to_atom/1 (atom table exhaustion). Unknown methods get a 501.
  @http_methods %{
    "get" => :get, "head" => :head, "post" => :post, "put" => :put,
    "delete" => :delete, "options" => :options, "trace" => :trace,
    "patch" => :patch
  }

  def start_link(opts) do
    server = Keyword.fetch!(opts, :server)
    local = normalize_local(Keyword.fetch!(opts, :local))
    token = opts[:token]
    subdomain = opts[:subdomain]
    resume = opts[:resume]

    state = %{
      local: local,
      token: token,
      subdomain: subdomain,
      resume: resume,
      url: nil,
      streams: %{},
      owner: self(),
      url_waiter: nil
    }

    WebSockex.start_link(server, __MODULE__, state,
      extra_headers: [{"Sec-WebSocket-Protocol", "etunnel-v1"}],
      ssl_options: ssl_options(server, opts)
    )
  end

  # ssl_options pins peer + hostname verification with the OS CA store
  # (issue #50: WebSockex 0.5.x defaults to verify_none, i.e. MITM-able wss).
  def ssl_options(server, opts \\ []) do
    case Keyword.get(opts, :tls_verify, :verify_peer) do
      :verify_none ->
        [verify: :verify_none]

      _ ->
        host = server |> URI.parse() |> Map.fetch!(:host) |> to_charlist()

        [
          verify: :verify_peer,
          cacerts: :public_key.cacerts_get(),
          server_name_indication: host,
          depth: 3
        ]
    end
  end

  defp normalize_local(local) when is_integer(local), do: "127.0.0.1:#{local}"
  defp normalize_local(local) when is_binary(local) do
    if Regex.match?(~r/^\d+$/, local), do: "127.0.0.1:#{local}", else: local
  end

  @doc """
  Returns the public tunnel URL, or `{:error, reason}` if the welcome frame
  (or a server error frame) does not arrive within `timeout` ms.

  WebSockex processes are not GenServers - a GenServer.call lands in
  handle_info as a raw `{:"$gen_call", from, msg}` tuple, which we intercept
  and answer manually below (the old handle_call/3 clause was never
  dispatched; that is what the missing-callback @impl warning was about).
  """
  def url(pid, timeout \\ @url_timeout) do
    GenServer.call(pid, {:url, timeout}, timeout + 1_000)
  catch
    # The tunnel process died (e.g. stopped by an error frame); surface the
    # exit reason as a value instead of raising in the caller.
    :exit, reason -> {:error, reason}
  end

  def close(pid) do
    WebSockex.send_frame(pid, {:text, Jason.encode!(%{type: "bye", reason: "client closed"})})
    GenServer.stop(pid, :normal)
  catch
    :exit, _ -> :ok
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
        state = reply_url(state, w["url"])

        state =
          if tok = w["resumeToken"] do
            send(state.owner, {:etunnel_resume_token, tok})
            %{state | resume: tok}
          else
            state
          end

        {:ok, %{state | url: w["url"], url_waiter: nil}}

      {:ok, %{"type" => "error"} = e} ->
        IO.puts(:stderr, "etunnel: #{e["code"]}: #{e["message"]}")
        state = reply_url(state, {:error, {e["code"], e["message"]}})
        {:stop, {:error, e}, %{state | url_waiter: nil}}

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
              # Bodies accumulate as reversed iodata (O(1) per chunk), not
              # appended binaries (O(n) per chunk, O(n^2) per request).
              1 -> {:ok, update_in(state, [:streams, sid, :body], &[payload | &1])}
              2 ->
                state = update_in(state, [:streams], &Map.delete(&1, sid))
                body = st.body |> Enum.reverse() |> IO.iodata_to_binary()
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
  def handle_info(:etunnel_hello, state) do
    hello =
      %{type: "hello", proto: 1, client: "etunnel-elixir/1.4"}
      |> maybe_put(:subdomain, state.subdomain)
      |> maybe_put(:token, state.token)
      |> maybe_put(:resume, state.resume)
      |> Jason.encode!()

    {:reply, {:text, hello}, state}
  end

  def handle_info({:etunnel_send, frame}, state) do
    {:reply, frame, state}
  end

  # Raw GenServer.call interception (see url/1): WebSockex routes unknown
  # messages to handle_info, so a call arrives as {:"$gen_call", from, msg}.
  def handle_info({:"$gen_call", from, {:url, timeout}}, state) do
    case state.url do
      nil ->
        Process.send_after(self(), {:etunnel_url_timeout, from}, timeout)
        {:ok, %{state | url_waiter: from}}

      url ->
        GenServer.reply(from, url)
        {:ok, state}
    end
  end

  def handle_info({:etunnel_url_timeout, from}, state) do
    if state.url_waiter == from, do: GenServer.reply(from, {:error, :timeout})
    {:noreply, state}
  end

  def handle_info(_msg, state), do: {:ok, state}

  defp reply_url(state, value) do
    if from = state.url_waiter, do: GenServer.reply(from, value)
    state
  end

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
    main{max-width:600px;padding:2.5rem 1.3rem;width:100%}
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

    case Map.fetch(@http_methods, String.downcase(method)) do
      :error ->
        # Unknown method: no atom is created for server input (issue #50).
        send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 501, headers: %{"Content-Type" => ["text/plain"]}})}})
        send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})

      {:ok, method_atom} ->
        url = String.to_charlist("http://#{local}#{path}")

        request =
          if method in ["GET", "HEAD"] do
            {url, headers}
          else
            {url, headers, String.to_charlist("application/octet-stream"), body}
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
