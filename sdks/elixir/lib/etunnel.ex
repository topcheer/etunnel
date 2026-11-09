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
        send(ws_pid, {:etunnel_send, {:text, Jason.encode!(%{type: "res", streamId: sid, status: 502, headers: %{"Content-Type" => ["text/plain"]}})}})
        send(ws_pid, {:etunnel_send, {:binary, frame(0x01, sid, "etunnel: local service error: #{inspect(e)}\n")}})
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
