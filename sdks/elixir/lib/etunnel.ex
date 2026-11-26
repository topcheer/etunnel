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

  # #243 M2: Go-parity resource limits (sdk/stream.go: maxClientStreams=256,
  # SetReadLimit(128KiB)). A hostile or buggy server must not be able to OOM
  # the client: refuse streams over the cap with an RST, RST a stream whose
  # buffered request body passes @max_stream_body, drop the connection on a
  # ws message above the read limit.
  @max_streams 256
  @max_stream_body 16 * 1024 * 1024
  @max_ws_msg 128 * 1024
  # #243 M3: client-side keepalive (Go pingLoop parity) - ping every 30s and
  # drop the tunnel when neither pongs nor traffic arrive for 150s; without
  # it a NAT blackhole keeps the client believing the tunnel is alive.
  @ping_interval_ms 30_000
  @ping_deadline_ms 150_000

  # Fixed atom set: the server-controlled method string must never reach
  # String.to_atom/1 (atom table exhaustion). Unknown methods get a 501.
  @http_methods %{
    "get" => :get, "head" => :head, "post" => :post, "put" => :put,
    "delete" => :delete, "options" => :options, "trace" => :trace,
    "patch" => :patch
  }

  # --- multi-target local routing (PROTOCOL.md §2) ---------------------------
  # Longest prefix wins, segment-boundary matching; strip keeps the query
  # byte-for-byte; no match = 404 at the SDK edge.

  def normalize_routes(routes) when is_list(routes) do
    {out, _seen} =
      Enum.reduce(Enum.with_index(routes), {[], MapSet.new()}, fn {r, i}, {acc, seen} ->
        p = r[:prefix] || ""

        cond do
          p == "" ->
            raise ArgumentError, "etunnel: route[#{i}]: prefix is required (use \"/\" for the fallback route)"

          (r[:addr] || "") == "" ->
            raise ArgumentError, "etunnel: route[#{i}]: addr is required"

          not String.starts_with?(p, "/") ->
            raise ArgumentError, "etunnel: route[#{i}]: prefix \"#{p}\" must start with '/'"

          true ->
            t = if(p != "/", do: String.trim_trailing(p, "/"), else: p)

            if MapSet.member?(seen, t) do
              raise ArgumentError, "etunnel: route[#{i}]: duplicate prefix \"#{t}\""
            end

            {[Map.merge(r, %{prefix: t, strip: !!r[:strip]}) | acc], MapSet.put(seen, t)}
        end
      end)

    out |> Enum.reverse() |> Enum.sort_by(fn r -> -byte_size(r[:prefix]) end)
  end

  def route_for(routes, path_and_query) when is_list(routes) do
    {path, query} =
      case :binary.match(path_and_query, "?") do
        {i, _} ->
          {binary_part(path_and_query, 0, i),
           binary_part(path_and_query, i, byte_size(path_and_query) - i)}

        :nomatch ->
          {path_and_query, ""}
      end

    Enum.find_value(routes, fn r ->
      p = r[:prefix]

      if p == "/" or path == p or String.starts_with?(path, p <> "/") do
        stripped =
          if r[:strip] and p != "/" do
            if path == p,
              do: "/",
              else: binary_part(path, byte_size(p), byte_size(path) - byte_size(p))
          else
            path
          end

        {r[:addr], stripped <> query}
      end
    end)
  end

  def start_link(opts) do
    server = Keyword.fetch!(opts, :server)
    routes = if r = opts[:routes], do: normalize_routes(r), else: nil
    if routes == [], do: raise(ArgumentError, "etunnel: routes must not be empty")

    local =
      case Keyword.get(opts, :local) do
        nil ->
          # keep the pre-routes failure mode: fetch! raises the original KeyError
          if is_nil(routes), do: Keyword.fetch!(opts, :local)

        l ->
          normalize_local(l)
      end

    token = opts[:token]
    subdomain = opts[:subdomain]
    resume = opts[:resume]

    state = %{
      local: local,
      routes: routes,
      token: token,
      subdomain: subdomain,
      resume: resume,
      url: nil,
      streams: %{},
      owner: self(),
      url_waiter: nil,
      # #417-E30 (P0): the keepalive bookkeeping keys must exist in the
      # initial state - every handle_frame does %{state | last_activity: ..}
      # and the ping loop reads last_pong/last_activity, which raised
      # KeyError {:last_activity, nil} on the very first frame.
      last_activity: now_ms(),
      last_pong: now_ms()
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
          server_name_indication: host
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

  Single-waiter semantics: a concurrent second call replaces the first
  caller's registration (the first then returns `{:error, :exit}`).
  Coordinate concurrent resolution on the caller side (#64 note 5).

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
    # #243 M3: arm the keepalive timer and start the liveness clocks.
    Process.send_after(self(), :etunnel_ping, @ping_interval_ms)
    now = System.monotonic_time(:millisecond)
    {:ok, %{state | last_pong: now, last_activity: now}}
  end

  # #243 M2: 128KiB read limit - control JSON is small (frames cap at 32KiB),
  # so a bigger ws message is a protocol violation and the tunnel is torn
  # down (Go SetReadLimit parity).
  def handle_frame({:text, msg}, state) when byte_size(msg) > @max_ws_msg do
    IO.puts(:stderr, "etunnel: read limit exceeded, dropping connection")
    {:stop, :read_limit_exceeded, state}
  end

  @impl true
  def handle_frame({:text, msg}, state) do
    state = %{state | last_activity: System.monotonic_time(:millisecond)} # #243 M3
    case Jason.decode(msg) do
      {:ok, %{"type" => "welcome"} = w} ->
        # #338: PROTOCOL.md section 3 both-sides version gate - reject newer
        # protocols loudly (permanent failure ends the reconnect supervisor,
        # Go reconnect.go parity); a missing proto counts as v1.
        if is_integer(w["proto"]) and w["proto"] > 1 do
          IO.puts(:stderr, "etunnel: server speaks protocol v#{w["proto"]}; update the SDK")
          state = reply_url(state, {:error, {"proto_version", "server speaks protocol v#{w["proto"]}"}})
          {:stop, {:proto_version, w["proto"]}, %{state | url_waiter: nil}}
        else
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
        end

      {:ok, %{"type" => "error"} = e} ->
        IO.puts(:stderr, "etunnel: #{e["code"]}: #{e["message"]}")
        state = reply_url(state, {:error, {e["code"], e["message"]}})
        {:stop, {:error, e}, %{state | url_waiter: nil}}

      {:ok, %{"type" => "req", "streamId" => sid} = req} ->
        # #243 M2: refuse a replayed live id or a stream beyond the cap
        # (Go registerClientStream parity) and tell the server with an RST.
        if Map.has_key?(state.streams, sid) or map_size(state.streams) >= @max_streams do
          {:reply, {:binary, frame(0x03, sid, "")}, state}
        else
          {:ok, put_in(state, [:streams, sid], %{req: req, body: [], size: 0})}
        end

      {:ok, %{"type" => "ping"} = p} ->
        {:reply, {:text, Jason.encode!(%{type: "pong", ts: p["ts"] || 0})}, state}

      {:ok, %{"type" => "pong"}} ->
        {:ok, %{state | last_pong: System.monotonic_time(:millisecond)}} # #243 M3

      {:ok, %{"type" => "bye"}} ->
        {:ok, state}

      _ ->
        {:ok, state}
    end
  end

  # #243 M2: 128KiB read limit (see the text-frame guard above).
  def handle_frame({:binary, bin}, state) when byte_size(bin) > @max_ws_msg do
    IO.puts(:stderr, "etunnel: read limit exceeded, dropping connection")
    {:stop, :read_limit_exceeded, state}
  end

  def handle_frame({:binary, bin}, state) do
    state = %{state | last_activity: System.monotonic_time(:millisecond)} # #243 M3
    case bin do
      <<kind, sid::big-32, n::big-32, rest::binary>> ->
        payload = binary_part(rest, 0, min(n, byte_size(rest)))

        case Map.fetch(state.streams, sid) do
          {:ok, st} ->
            case kind do
              # Bodies accumulate as reversed iodata (O(1) per chunk), not
              # appended binaries (O(n) per chunk, O(n^2) per request).
              # #243 M2: bound the buffered request body; over the limit the
              # stream is refused with an RST instead of buffering the server
              # into our RAM.
              1 ->
                new_size = st.size + byte_size(payload)
                if new_size > @max_stream_body do
                  state = update_in(state, [:streams], &Map.delete(&1, sid))
                  {:reply, {:binary, frame(0x03, sid, "")}, state}
                else
                  state = update_in(state, [:streams, sid, :body], &[payload | &1])
                  {:ok, update_in(state, [:streams, sid, :size], &(&1 + byte_size(payload)))}
                end
              2 ->
                state = update_in(state, [:streams], &Map.delete(&1, sid))
                body = st.body |> Enum.reverse() |> IO.iodata_to_binary()
                me = self()
                Task.start(fn -> serve(me, sid, st.req, body, state.local, state.routes) end)
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
      %{type: "hello", proto: 1, client: "etunnel-elixir/" <> Mix.Project.config()[:version], version: Mix.Project.config()[:version]}
      |> maybe_put(:subdomain, state.subdomain)
      |> maybe_put(:token, state.token)
      |> maybe_put(:resume, state.resume)
      |> Jason.encode!()

    {:reply, {:text, hello}, state}
  end

  def handle_info({:etunnel_send, frame}, state) do
    {:reply, frame, state}
  end

  # #243 M3: keepalive tick - ping the server; when neither a pong nor any
  # traffic has been seen for 150s the tunnel is dead (NAT blackhole) and the
  # process is stopped so the supervisor can reconnect.
  def handle_info(:etunnel_ping, state) do
    Process.send_after(self(), :etunnel_ping, @ping_interval_ms)
    now = System.monotonic_time(:millisecond)
    if now - state.last_pong > @ping_deadline_ms and now - state.last_activity > @ping_deadline_ms do
      IO.puts(:stderr, "etunnel: ping timeout")
      {:stop, :ping_timeout, state}
    else
      {:reply, {:text, Jason.encode!(%{type: "ping", ts: System.system_time(:second)})}, state}
    end
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

  defp now_ms, do: System.monotonic_time(:millisecond)

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
    <footer>powered by <a href="https://etun.uk">etunnel</a></footer>
    </main></body></html>
    """
  end

  defp serve(ws_pid, sid, req, body, local, routes \\ nil) do
    method = req["method"] || "GET"
    path = req["path"] || "/"

    {local, path} =
      if routes do
        case route_for(routes, path) do
          nil ->
            # no route matched: 404 at the SDK edge, nothing dialed
            send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 404, headers: %{"Content-Type" => ["text/plain; charset=utf-8"]}})}})
            send(ws_pid, {:etunnel_send, {:binary, frame(0x01, sid, "404 page not found (no route matches this path)\n")}})
            send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})
            {nil, nil}

          {addr, p} ->
            {addr, p}
        end
      else
        {local, path}
      end

    headers =
      (req["headers"] || %{})
      |> Enum.reject(fn {k, _v} -> String.downcase(k) in @hop end)
      |> Enum.flat_map(fn {k, vs} -> Enum.map(vs, &{String.to_charlist(k), String.to_charlist(&1)}) end)

    case Map.fetch(@http_methods, String.downcase(method)) do
      :error ->
        # Unknown method: no atom is created for server input (issue #50).
        send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 501, headers: %{"Content-Type" => ["text/plain"]}})}})
        send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})

      {:ok, method_atom} when not is_nil(local) ->
        url = String.to_charlist("http://#{local}#{path}")

        request =
          if method in ["GET", "HEAD"] do
            {url, headers}
          else
            {url, headers, String.to_charlist("application/octet-stream"), body}
          end

        # Default :httpc profile honours http_proxy/https_proxy env vars,
        # which can hijack the local (127.0.0.1) hop through a corporate
        # proxy (#64 note 2): disable proxying explicitly for these calls.
        http_opts = [{:timeout, 120_000}, {:proxy, {:undefined, []}}]
        case :httpc.request(method_atom, request, http_opts, [{:body_format, :binary}, {:sync, true}]) do
          {:ok, {{_, status, _}, resp_headers, out}} ->
            hdrs =
              resp_headers
              |> Enum.reject(fn {k, _} -> to_string(k) |> String.downcase() |> then(&(&1 in @hop)) end)
              # #243 M6: group_by keeps repeated headers (Set-Cookie) as
              # arrays instead of Map.new keeping only the last value.
              |> Enum.group_by(fn {k, _} -> to_string(k) end, fn {_, v} -> to_string(v) end)

            send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: status, headers: hdrs})}})
            send_chunks(ws_pid, sid, out || "", 0)
            send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})

          {:error, e} ->
            send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 502, headers: %{"Content-Type" => ["text/html; charset=utf-8"]}})}})
            send(ws_pid, {:etunnel_send, {:binary, frame(0x01, sid, local_error_page(inspect(e)))}})
            send(ws_pid, {:etunnel_send, {:binary, frame(0x02, sid, "")}})
        end

      {:ok, _} ->
        :ok
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

  # --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
  #   creds = Etunnel.login("https://t.etun.uk")     # opens the browser
  #   # -> %{api_token: ..., subdomains: [...], plan: ...}. Persist it and
  #   #    pass api_token as the token opt of future Etunnel.connect calls.
  def login(server, open_browser \\ nil, timeout \\ 600) do
    base = String.trim_trailing(server || "", "/")
    if base == "", do: raise(ArgumentError, message: "etunnel: login: server required")

    {:ok, srv} = :gen_tcp.listen(0, [:binary, packet: :raw, active: false, ip: {127, 0, 0, 1}, reuseaddr: true])
    {:ok, port} = :inet.port(srv)

    start = login_post(base, "/auth/cli/start", %{redirect_uri: "http://127.0.0.1:#{port}/callback"})
    auth_url = start["auth_url"]
    device_code = start["device_code"]
    unless auth_url && device_code, do: raise(RuntimeError, message: "etunnel: login: empty start payload")

    opened =
      cond do
        open_browser -> open_browser.(auth_url); true
        true ->
          {uname, 0} = System.cmd("uname", [], stderr_to_stdout: true)
          cmd = if String.trim(uname) == "Darwin", do: "open", else: "xdg-open"
          case System.cmd(cmd, [auth_url], stderr_to_stdout: true) do
            {_out, 0} -> true
            _ -> false
          end
      end
    unless opened, do: IO.puts(:stderr, "etunnel: authorize in your browser: #{auth_url}")

    deadline = System.monotonic_time(:second) + timeout
    code = login_await_code(srv, deadline)
    :gen_tcp.close(srv)
    if code == "", do: raise(RuntimeError, message: "etunnel: login: callback without code")

    creds = login_post(base, "/auth/cli/exchange", %{device_code: device_code, code: code})
    unless creds["api_token"] not in [nil, ""], do: raise(RuntimeError, message: "etunnel: login: no api_token in exchange response")
    %{api_token: creds["api_token"], subdomains: creds["subdomains"] || [], plan: creds["plan"] || ""}
  end

  defp login_await_code(srv, deadline) do
    if System.monotonic_time(:second) > deadline do
      raise RuntimeError, message: "etunnel: login timed out"
    end
    case :gen_tcp.accept(srv, 1000) do
      {:ok, sock} ->
        {:ok, req} = :gen_tcp.recv(sock, 0, 5000)
        path = req |> to_string |> String.split(" ") |> Enum.at(1) || ""
        code =
          path
          |> String.split("?")
          |> Enum.at(1, "")
          |> String.split("&")
          |> Enum.find("", &String.starts_with?(&1, "code="))
          |> String.trim_leading("code=")
          |> URI.decode()
        body = "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>"
        :gen_tcp.send(sock, "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: #{byte_size(body)}\r\nConnection: close\r\n\r\n#{body}")
        :gen_tcp.close(sock)
        if code == "", do: login_await_code(srv, deadline), else: code
      {:error, :timeout} -> login_await_code(srv, deadline)
      {:error, other} -> raise RuntimeError, message: "etunnel: login: accept failed: #{inspect(other)}"
    end
  end

  defp login_post(base, path, body) do
    :application.ensure_all_started(:inets)
    :application.ensure_all_started(:ssl)
    url = base <> path
    req = {to_charlist(url), [], ~c"application/json", Jason.encode!(body)}
    case :httpc.request(:post, req, [{:timeout, 30_000}, {:proxy, {:undefined, []}}], body_format: :binary) do
      {:ok, {{_, 200, _}, _, resp}} -> Jason.decode!(resp)
      {:ok, {{_, status, _}, _, resp}} ->
        raise RuntimeError, message: "etunnel: login: HTTP #{status} from #{path}: #{String.slice(to_string(resp), 0, 200)}"
      {:error, why} -> raise RuntimeError, message: "etunnel: login: request to #{path} failed: #{inspect(why)}"
    end
  end

end
