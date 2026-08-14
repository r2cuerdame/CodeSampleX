defmodule CsxEcto.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_ecto,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # :ecto only. Ecto.Changeset, Ecto.Schema and embedded_schema all live in
    # this package; ecto_sql is the separate package that adds Ecto.Adapters.SQL
    # and the migration tooling, and nothing here needs it. The contract
    # asserts that ecto_sql really is absent, so "you can validate without a
    # database" is measured rather than claimed.
    #
    # ecto pulls in :decimal and :telemetry as required dependencies. :jason is
    # an optional dependency, needed only for Ecto's JSON-typed fields, so it
    # never lands in the lock here.
    [{:ecto, "3.14.1"}]
  end
end
