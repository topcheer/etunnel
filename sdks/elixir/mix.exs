defmodule Etunnel.MixProject do
  use Mix.Project

  def project do
    [
      app: :etunnel,
      version: "1.0.0",
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
    "Expose a local service through an etunnel reverse-tunnel server with one line of code"
  end

  defp package do
    [
      files: ["lib", "mix.exs", "README.md", "LICENSE"],
      licenses: ["MIT"],
      links: %{"GitHub" => "https://github.com/ggcode/etunnel"}
    ]
  end

  defp docs do
    [main: "readme", extras: ["README.md"]]
  end
end
