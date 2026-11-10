defmodule EtunnelTest do
  # Issue #50 unit tests: TLS defaults, resume chain state, structured error
  # stop, and atom safety. The live-server acceptance (subdomain stability
  # across reconnect, self-signed rejection) lives in etunnel_integration_test.exs.
  use ExUnit.Case, async: true

  test "ssl_options verifies peers with the OS CA store by default" do
    opts = Etunnel.ssl_options("wss://t.etun.uk/tunnel", [])
    assert Keyword.get(opts, :verify) == :verify_peer
    assert Keyword.get(opts, :cacerts) == :public_key.cacerts_get()
    assert Keyword.get(opts, :server_name_indication) == String.to_charlist("t.etun.uk")
  end

  test "ssl_options extracts the host from host:port servers" do
    assert Etunnel.ssl_options("wss://t.example.com:443/tunnel", [])
           |> Keyword.get(:server_name_indication) == String.to_charlist("t.example.com")
  end

  test "tls_verify: :verify_none is an explicit opt-out only" do
    assert Etunnel.ssl_options("wss://t.etun.uk/tunnel", tls_verify: :verify_none) ==
             [verify: :verify_none]
  end

  defp fresh_state(owner) do
    %{local: "127.0.0.1:1", token: nil, subdomain: nil, resume: nil,
      url: nil, streams: %{}, owner: owner, url_waiter: nil}
  end

  test "hello carries the resume token from start_link state" do
    me = self()

    spawn(fn ->
      state = fresh_state(me) |> Map.put(:resume, "tok-123")
      {:reply, {:text, frame}, _} = Etunnel.handle_info(:etunnel_hello, state)
      send(me, {:hello, frame})
    end)

    assert_receive {:hello, frame}, 2_000
    hello = Jason.decode!(frame)
    assert %{"type" => "hello", "proto" => 1} = hello
    assert hello["resume"] == "tok-123"
  end

  test "hello omits resume when none is set" do
    me = self()

    spawn(fn ->
      {:reply, {:text, frame}, _} = Etunnel.handle_info(:etunnel_hello, fresh_state(me))
      send(me, {:hello, frame})
    end)

    assert_receive {:hello, frame}, 2_000
    refute Map.has_key?(Jason.decode!(frame), "resume")
  end

  test "welcome stores url, keeps the resume token and notifies the owner" do
    state = fresh_state(self())
    welcome = Jason.encode!(%{"type" => "welcome", "url" => "https://abc.t.test", "resumeToken" => "tok-9"})

    {:ok, state} = Etunnel.handle_frame({:text, welcome}, state)
    assert state.url == "https://abc.t.test"
    assert state.resume == "tok-9"
    assert_received {:etunnel_resume_token, "tok-9"}
  end

  test "welcome without a resume token sends nothing" do
    state = fresh_state(self())
    {:ok, state} = Etunnel.handle_frame({:text, ~s({"type":"welcome","url":"https://x.t.test"})}, state)
    assert state.resume == nil
    refute_received {:etunnel_resume_token, _}
  end

  test "server error frames stop the process with a structured reason" do
    state = fresh_state(self())
    err = Jason.encode!(%{"type" => "error", "code" => 4, "message" => "too many tunnels"})

    assert {:stop, {:error, %{"code" => 4, "message" => "too many tunnels"}}, _} =
             Etunnel.handle_frame({:text, err}, state)
  end

  test "the serve path creates no atoms from server input" do
    code = File.read!("lib/etunnel.ex")
    refute code =~ "String.to_atom("
    assert code =~ "@http_methods"
  end
end
