defmodule CsxNimbleCsv.Parsers do
  @moduledoc """
  Holds the NimbleCSV.define/2 calls for this project.

  A parser IS a module. NimbleCSV.define/2 writes it, and the separator,
  escape and line_separator settings are baked into the generated code, so
  there is no parser struct to thread through your own functions; the only
  per-call option is :skip_headers.

  The calls live at the top of this file, outside any function, so they run
  once while it compiles. That placement is a visibility and redefinition
  rule rather than a technical requirement — define/2 is an ordinary function
  that also works at runtime, and test/contract_test.exs measures both the
  function-not-macro fact and what the second call costs.
  """

  # A module body executes at compile time, so this call runs during
  # compilation of this file and CsxNimbleCsv.SemicolonCSV exists in the
  # BEAM from then on.
  #
  # :separator accepts a list. Every entry is accepted when parsing, but only
  # the FIRST is used when dumping, so this parser reads both ";" and "," and
  # always writes ";". :line_separator is spelled out even though "\n" is
  # already the library default, because NimbleCSV.RFC4180 overrides it to
  # "\r\n" and the contract dumps both parsers side by side.
  NimbleCSV.define(CsxNimbleCsv.SemicolonCSV,
    separator: [";", ","],
    escape: "\"",
    line_separator: "\n"
  )

  # The trap in the line below is the module NAME. NimbleCSV.define/2 is an
  # ordinary function that receives an already-resolved module atom, so the
  # alias is expanded by the caller and the automatic nesting that `defmodule
  # Foo` gets inside `defmodule Bar` does not happen here. Writing a bare
  # alias inside this module creates the TOP-LEVEL module Elixir.LooseCSV,
  # not CsxNimbleCsv.Parsers.LooseCSV. The contract asserts both halves.
  NimbleCSV.define(LooseCSV, separator: ",", escape: "'")
end
