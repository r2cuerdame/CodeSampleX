defmodule CsxReq.MixProject do
  use Mix.Project

  def project do
    [
      app: :csx_req,
      version: "0.1.0",
      elixir: "~> 1.15",
      deps: deps()
    ]
  end

  def application, do: [extra_applications: []]

  defp deps do
    # Req.Test ships inside req itself, so stubbing HTTP needs no extra test
    # library and no fake server process. What it does need is plug, and plug
    # is where people get caught: req declares it `optional: true`, and both
    # Req.Plug and every Req.Test helper are wrapped in
    # `if Code.ensure_loaded?(Plug)` evaluated at req's compile time. Leave
    # plug out of your deps and nothing fails to compile — you get a runtime
    # `raise "missing plug dependency"` from inside the stub instead. So plug
    # is listed here explicitly even though no line of this project calls it
    # directly except Plug.Conn.send_resp in the stubs.
    #
    # jason arrives as a hard dependency of req and is req's built-in JSON
    # codec. It is named here too because req 0.7.0 deprecated
    # `decode_json: opts` in favour of `decoders: [json: &Jason.decode(&1,
    # opts)]`, which puts Jason in your own source and therefore in your own
    # deps.
    [{:req, "0.7.2"}, {:plug, "1.20.3"}, {:jason, "1.4.5"}]
  end
end
