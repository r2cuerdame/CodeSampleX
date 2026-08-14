defmodule CsxPlug.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_plug,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # Plug.Test and Plug.Router both ship inside plug itself, so testing a
    # pipeline needs no extra test dependency and no web server. Jason is
    # here because Plug.Parsers.JSON has no built-in decoder: the parser is
    # a shell that calls whatever :json_decoder you hand it.
    [{:plug, "1.20.3"}, {:jason, "1.4.5"}]
  end
end
