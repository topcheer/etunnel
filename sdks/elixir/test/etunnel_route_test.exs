defmodule Etunnel.RouteTest do
  @moduledoc """
  Route table semantics (same table as sdks/node/route.js).
  Run: mix test test/etunnel_route_test.exs
  """
  use ExUnit.Case, async: true

  test "boundaries + longest prefix wins" do
    rs =
      Etunnel.normalize_routes([
        %{prefix: "/", addr: "fe:1"},
        %{prefix: "/api", addr: "be:1"},
        %{prefix: "/api/v2", addr: "be2:1"}
      ])

    assert {"fe:1", _} = Etunnel.route_for(rs, "/apiv2")
    assert {"fe:1", _} = Etunnel.route_for(rs, "/apiary")
    assert {"be:1", _} = Etunnel.route_for(rs, "/api")
    assert {"be:1", _} = Etunnel.route_for(rs, "/api/u")
    assert {"be2:1", _} = Etunnel.route_for(rs, "/api/v2/x?q")
  end

  test "strip keeps the query byte-for-byte" do
    rs =
      Etunnel.normalize_routes([
        %{prefix: "/api", addr: "be:1", strip: true},
        %{prefix: "/", addr: "fe:1", strip: true}
      ])

    assert {"be:1", "/u?x=1&y=2"} = Etunnel.route_for(rs, "/api/u?x=1&y=2")
    assert {"be:1", "/"} = Etunnel.route_for(rs, "/api")
    assert {"fe:1", "/app?c=1"} = Etunnel.route_for(rs, "/app?c=1")
  end

  test "no match returns nil" do
    rs = Etunnel.normalize_routes([%{prefix: "/api", addr: "be:1"}])
    assert Etunnel.route_for(rs, "/other") == nil
  end

  test "validation" do
    assert_raise ArgumentError, ~r/addr is required/, fn ->
      Etunnel.normalize_routes([%{prefix: "/", addr: ""}])
    end

    assert_raise ArgumentError, ~r/must start with/, fn ->
      Etunnel.normalize_routes([%{prefix: "api", addr: "a:1"}])
    end

    assert_raise ArgumentError, ~r/duplicate/, fn ->
      Etunnel.normalize_routes([%{prefix: "/api", addr: "a:1"}, %{prefix: "/api/", addr: "a:2"}])
    end
  end
end
