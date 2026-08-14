defmodule CsxJson.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_json,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    [{:jason, "1.4.4"}]
  end
end
