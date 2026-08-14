defmodule CsxDecimal.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_decimal,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    [{:decimal, "3.1.1"}]
  end
end
