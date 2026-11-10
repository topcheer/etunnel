defmodule EtunnelIntegrationTest do
  # Issue #50 acceptance, against a real etunneld built from this worktree:
  #  (2) reconnect with the resume token reclaims the SAME subdomain
  #  (1) verify_peer rejects a self-signed server certificate
  # plus (4) close/1 tears the process down.
  use ExUnit.Case, async: false

  @repo_root Path.expand("../../..", __DIR__)
  @tmp Path.join(System.tmp_dir(), "etunnel-elixir-test-#{System.unique_integer([:positive])}")
  @domain "t.test"

  setup_all do
    File.mkdir_p!(@tmp)
    bin = Path.join(@tmp, "etunneld")

    {_, 0} =
      System.cmd("go", ["build", "-o", bin, "./cmd/etunneld"], cd: @repo_root,
        stderr_to_stdout: true)

    {_, 0} =
      System.cmd("openssl", [
        "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", Path.join(@tmp, "key.pem"),
        "-out", Path.join(@tmp, "cert.pem"),
        "-days", "2", "-subj", "/CN=#{@domain}"
      ], stderr_to_stdout: true)

    ports = free_ports(6)
    http_server = start_server!(bin, Enum.at(ports, 0), Enum.at(ports, 1), Enum.at(ports, 2), [])
    tls_server =
      start_server!(bin, Enum.at(ports, 3), Enum.at(ports, 4), Enum.at(ports, 5),
        tls_static: {Path.join(@tmp, "cert.pem"), Path.join(@tmp, "key.pem")})

    wait_ready!(Enum.at(ports, 0))
    wait_ready!(Enum.at(ports, 3))

    on_exit(fn ->
      for p <- [http_server, tls_server] do
        if Port.info(p), do: Port.close(p)
      end
      File.rm_rf!(@tmp)
    end)

    %{
      ws_url: "ws://127.0.0.1:#{Enum.at(ports, 0)}/tunnel",
      tls_url: "wss://127.0.0.1:#{Enum.at(ports, 3)}/tunnel"
    }
  end

  test "reconnect with the resume token keeps the same subdomain", ctx do
    local = start_local_echo!()
    {:ok, t1} = Etunnel.start_link(server: ctx.ws_url, local: local)
    url1 = Etunnel.url(t1)
    assert is_binary(url1)

    assert_received {:etunnel_resume_token, tok}
    assert is_binary(tok) and byte_size(tok) > 10

    # hard kill: no bye, the token is the only survivor. Unlink first so
    # the linked test process does not receive the :kill exit too.
    Process.unlink(t1)
    Process.exit(t1, :kill)

    {:ok, t2} = Etunnel.start_link(server: ctx.ws_url, local: local, resume: tok)
    url2 = Etunnel.url(t2)
    assert is_binary(url2)
    assert url2 == url1, "subdomain changed across reconnect: #{url1} -> #{url2}"

    Etunnel.close(t2)
    refute Process.alive?(t2)
  end

  test "close/1 sends bye and terminates the tunnel process", ctx do
    local = start_local_echo!()
    {:ok, t} = Etunnel.start_link(server: ctx.ws_url, local: local)
    assert is_binary(Etunnel.url(t))
    Etunnel.close(t)
    refute Process.alive?(t)
    assert Etunnel.close(t) == :ok
  end

  test "verify_peer rejects a self-signed certificate", ctx do
    local = start_local_echo!()

    assert {:error, _} = Etunnel.start_link(server: ctx.tls_url, local: local)

    # control: the same server accepts with the explicit (test-only) opt-out
    {:ok, t} = Etunnel.start_link(server: ctx.tls_url, local: local, tls_verify: :verify_none)
    assert is_binary(Etunnel.url(t))
    Etunnel.close(t)
  end

  # ---- helpers ---------------------------------------------------------------

  defp free_ports(n) do
    for _ <- 1..n do
      {:ok, l} = :gen_tcp.listen(0, [reuseaddr: true])
      {:ok, port} = :inet.port(l)
      :gen_tcp.close(l)
      port
    end
  end

  # Poll until the tunnel server accepts TCP on its main port (the Go binary
  # needs a moment to bind after Port.open returns).
  defp wait_ready!(port, deadline \\ nil) do
    deadline = deadline || (System.monotonic_time(:millisecond) + 5_000)

    case :gen_tcp.connect(~c"127.0.0.1", port, [], 100) do
      {:ok, s} ->
        :gen_tcp.close(s)
        :ok

      {:error, _} ->
        if System.monotonic_time(:millisecond) > deadline do
          flunk("etunneld never became ready on port #{port}")
        else
          Process.sleep(50)
          wait_ready!(port, deadline)
        end
    end
  end

  defp start_server!(bin, ws, http, admin, opts) do
    base = [
      "-domain", @domain,
      "-addr", "127.0.0.1:#{ws}",
      "-http-addr", "127.0.0.1:#{http}",
      "-admin-addr", "127.0.0.1:#{admin}",
      "-admin-token", "test",
      "-accounts", Path.join(@tmp, "accounts-#{ws}.json"),
      "-ws-path", "/tunnel"
    ]

    args =
      case Keyword.fetch(opts, :tls_static) do
        {:ok, {cert, key}} -> base ++ ["-tls-mode", "static", "-tls-cert", cert, "-tls-key", key]
        :error -> base ++ ["-tls-mode", "http"]
      end

    Port.open({:spawn_executable, bin}, [:binary, :exit_status, :stderr_to_stdout, args: args, cd: @tmp])
  end

  defp start_local_echo! do
    {:ok, l} = :gen_tcp.listen(0, [reuseaddr: true, active: false])
    {:ok, port} = :inet.port(l)
    spawn(fn -> echo_loop(l) end)
    port
  end

  defp echo_loop(l) do
    case :gen_tcp.accept(l, 300_000) do
      {:ok, s} ->
        spawn(fn -> echo_serve(s) end)
        echo_loop(l)

      {:error, :timeout} -> :gen_tcp.close(l)
      {:error, :closed} -> :ok
    end
  end

  defp echo_serve(s) do
    case :gen_tcp.recv(s, 0, 10_000) do
      {:ok, _data} ->
        body = "hello from etunnel-elixir-test"
        :gen_tcp.send(s, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: #{byte_size(body)}\r\nConnection: close\r\n\r\n#{body}")
        :gen_tcp.close(s)

      _ -> :gen_tcp.close(s)
    end
  end
end
