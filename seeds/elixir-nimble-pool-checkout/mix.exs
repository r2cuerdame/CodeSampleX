defmodule CsxNimblePool.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_nimble_pool,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # nimble_pool declares no dependencies of its own, so mix.lock holds a
    # single entry. NimblePool itself is a GenServer whose bookkeeping is
    # process monitors, which is why the failure paths below hinge on who is
    # being monitored rather than on any supervision strategy.
    [{:nimble_pool, "1.1.0"}]
  end
end
