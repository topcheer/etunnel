defmodule Etunnel.App do
  use Application

  def start(_type, _args) do
    # :inets provides :httpc for local requests; no extra children needed.
    Supervisor.start_link([], strategy: :one_for_one)
  end
end
