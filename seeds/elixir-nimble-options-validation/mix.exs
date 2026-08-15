defmodule CsxNimbleOptions.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_nimble_options,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # nimble_options has no dependencies at all, which is the reason so many
    # libraries adopted it for their option lists. Nothing else is needed to
    # run this seed.
    [{:nimble_options, "1.1.1"}]
  end
end
