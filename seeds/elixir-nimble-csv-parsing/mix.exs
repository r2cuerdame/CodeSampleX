defmodule CsxNimbleCsv.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_nimble_csv,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # nimble_csv has no runtime dependencies at all, so the lock below is a
    # single entry. Everything it does is code generation: NimbleCSV.define/2
    # writes a parser module, and the separator and escape settings are baked
    # into that module instead of being passed around at runtime. define/2 is
    # an ordinary function and not a macro, which test/contract_test.exs
    # measures — "compile time" is where this project calls it, not a
    # restriction the library imposes.
    [{:nimble_csv, "1.3.0"}]
  end
end
