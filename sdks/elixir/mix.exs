defmodule Etunnel.MixProject do
  use Mix.Project

  def project do
    [
      app: :etunnel,
      version: "1.6.2",
      elixir: "~> 1.12",
      start_permanent: Mix.env() == :prod,
      deps: deps(),
      description: description(),
      package: package(),
      docs: docs()
    ]
  end

  def application do
    [extra_applications: [:logger, :ssl, :inets], mod: {Etunnel.App, []}]
  end

  defp deps do
    [
      {:websockex, "~> 0.4"},
      {:jason, "~> 1.3"},
      {:ex_doc, "~> 0.31", only: :dev, runtime: false}
    ]
  end

  defp description do
    "Expose local services to the internet in one line of code: instant public HTTPS URLs with automatic TLS, through NAT and firewalls, no signup. Webhook-friendly ngrok-style reverse tunnel, with SDKs in 20 languages and a single-binary Go server."
  end

  defp package do
    [
      files: ["lib", "mix.exs", "README.md", "LICENSE"],
      licenses: ["MIT"],
      links: %{"GitHub" => "https://github.com/topcheer/etunnel"}
    ]
  end

  defp docs do
    [main: "readme", extras: ["README.md"]]
  end
end
