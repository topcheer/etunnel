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
#   {:ok, t} = Etunnel.start_link(server: "wss://t.ggcode.uk/tunnel", local_port: 3000)
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
      subdomain: opts[:subdomain]
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
      |> Jason.encode!()

    {:reply, {:text, hello}, state}
  end

  def handle_info(:hello_sent, state), do: {:ok, state}

  @impl true
  def handle_frame({:text, msg}, state) do
    case Jason.decode(msg) do
      {:ok, %{"type" => "welcome"} = w} ->
        send(state.from, {:etunnel_up, w["url"]})
        {:ok, %{state | url: w["url"]}}

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
            headers: %{"Content-Type" => ["text/plain"]}})
          send(self(), {:send, res, "etunnel: local service error: #{inspect(e)}\n"})
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
