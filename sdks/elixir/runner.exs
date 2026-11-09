# etunnel CLI runner: connects to the server and supervises the tunnel,
# reconnecting with exponential backoff after a disconnect. The SDK itself
# prints `TUNNEL_URL: ...` on every welcome frame, so a reconnect re-announces
# the URL.
#
#   cd sdks/elixir && mix run runner.exs 3000
#
# Env: ETUNNEL_SERVER, ETUNNEL_LOCAL, ETUNNEL_TOKEN, ETUNNEL_SUBDOMAIN;
#      ETUNNEL_RECONNECT=0 disables the reconnect supervisor.

defmodule EtunnelRunner do
  @min_backoff 500
  @max_backoff 30_000
  # how long a fresh connection may take to turn into a usable tunnel
  @connect_timeout 10_000

  def run(conf) do
    Process.flag(:trap_exit, true)
    # The tunnel process links to us, so trapping exits is what lets the
    # supervisor observe a disconnect instead of dying with it.
    trap(:sigterm)
    # SIGINT cannot be trapped inside the BEAM (it belongs to the break
    # handler), so it keeps its default disposition: the VM exits immediately
    # and the supervisor never reconnects - the required stop semantics.
    loop(conf, @min_backoff)
  end

  defp trap(signal) do
    me = self()
    System.trap_signal(signal, fn -> send(me, :etunnel_stop) end)
  rescue
    e -> IO.puts(:stderr, "etunnel: cannot trap #{signal}: #{Exception.message(e)}")
  end

  defp loop(conf, backoff) do
    conf = %{conf | resume: Process.get(:etunnel_resume, conf.resume)}
    case start(conf) do
      {:ok, pid} ->
        case watch(pid) do
          :stopped ->
            shutdown(pid)

          {:up, :stopped} ->
            shutdown(pid)

          {:up, :down} ->
            # a live tunnel that dropped: a success, so the backoff resets
            shutdown(pid)
            retry(conf, @min_backoff)

          :down ->
            shutdown(pid)
            retry(conf, backoff)

          :timeout ->
            IO.puts(:stderr, "etunnel: no tunnel url after #{@connect_timeout}ms")
            shutdown(pid, :kill)
            retry(conf, backoff)
        end

      {:error, reason} ->
        IO.puts(:stderr, "etunnel: connect failed: #{inspect(reason)}")
        retry(conf, backoff)
    end
  end

  defp start(conf) do
    Etunnel.start_link(
      server: conf.server,
      local: conf.local,
      token: conf.token,
      subdomain: conf.subdomain,
      resume: conf.resume
    )
  end

  # Waits for the connection to become a tunnel (welcome seen), then for that
  # tunnel to go away. Every terminal state is returned so the caller can back
  # off; nothing here retries on its own.
  defp watch(pid), do: watch(pid, System.monotonic_time(:millisecond) + @connect_timeout)

  defp watch(pid, deadline) do
    receive do
      :etunnel_stop -> :stopped
      {:etunnel_resume_token, tok} ->
        Process.put(:etunnel_resume, tok) # kept for the next start()
        watch(pid, deadline)
      {:EXIT, ^pid, _reason} -> :down
    after
      100 ->
        cond do
          up?(pid) -> await_down(pid)
          System.monotonic_time(:millisecond) >= deadline -> :timeout
          true -> watch(pid, deadline)
        end
    end
  end

  defp await_down(pid) do
    receive do
      :etunnel_stop -> {:up, :stopped}
      {:EXIT, ^pid, _reason} -> {:up, :down}
    end
  end

  # The SDK publishes the URL once the welcome frame arrived; a dead process
  # races with us, so a failed call simply means "not up".
  defp up?(pid) do
    Etunnel.url(pid) != nil
  catch
    :exit, _ -> false
  end

  defp shutdown(pid, reason \\ :shutdown) do
    if Process.alive?(pid), do: Process.exit(pid, reason)

    receive do
      {:EXIT, ^pid, _reason} -> :ok
    after
      1_000 -> :ok
    end
  end

  defp retry(conf, backoff) do
    if conf.reconnect do
      wait_ms = jitter(backoff)
      IO.puts(:stderr, "etunnel: reconnecting in #{wait_ms}ms")

      if sleep?(wait_ms) do
        loop(conf, next_backoff(backoff))
      else
        :ok
      end
    else
      IO.puts(:stderr, "etunnel: giving up (ETUNNEL_RECONNECT=0)")
      :ok
    end
  end

  defp jitter(ms), do: round(ms * (0.8 + 0.4 * :rand.uniform()))

  defp next_backoff(ms), do: min(ms * 2, @max_backoff)

  # receive/after wakes up as soon as :etunnel_stop arrives, so a signal during
  # the backoff wait stops the runner immediately.
  defp sleep?(ms) do
    receive do
      :etunnel_stop -> false
    after
      ms -> true
    end
  end
end

local = List.first(System.argv()) || System.get_env("ETUNNEL_LOCAL") || "3000"

conf = %{
  server: System.get_env("ETUNNEL_SERVER") || "wss://t.ggcode.uk/tunnel",
  local: local,
  token: System.get_env("ETUNNEL_TOKEN"),
  subdomain: System.get_env("ETUNNEL_SUBDOMAIN"),
  # server-signed token: reclaimed on reconnect so the subdomain stays put
  resume: nil,
  reconnect: System.get_env("ETUNNEL_RECONNECT") != "0"
}

EtunnelRunner.run(conf)
