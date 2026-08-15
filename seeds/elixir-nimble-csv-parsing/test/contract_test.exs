defmodule CsxNimbleCsvTest do
  use ExUnit.Case

  # NimbleCSV.RFC4180 is defined by nimble_csv itself, by a NimbleCSV.define/2
  # call sitting at the bottom of lib/nimble_csv.ex — the same call this
  # project makes in lib/csx_nimble_csv/parsers.ex. Aliasing it to CSV is the
  # usage the README recommends.
  alias NimbleCSV.RFC4180, as: CSV
  alias CsxNimbleCsv.SemicolonCSV

  # MEASURED, and it contradicts the way this library is usually described.
  #
  # NimbleCSV.define/2 is documented as something you call "at the top of your
  # file and not inside a function", which reads like it must be a macro that
  # only works at compile time. It is not: it is a plain `def` whose body is a
  # `defmodule`, so it is an ordinary runtime function call that happens to
  # create a module. Everything below is measurement, not the docs.
  test "define/2 is a function, not a macro, and calling it twice redefines the module" do
    # Code.ensure_loaded! first, or this test passes and fails depending on
    # what else ran. function_exported?/3 and macro_exported?/3 both answer
    # only for modules already loaded into the VM, and on a run that does not
    # recompile this project nothing loads NimbleCSV itself — the generated
    # parsers are separate modules, and NimbleCSV is loaded only while a file
    # that calls define/2 is being compiled. Measured on a warm _build:
    # :erlang.module_loaded(NimbleCSV) is false and function_exported?/3
    # answers false until the line below runs.
    Code.ensure_loaded!(NimbleCSV)
    assert function_exported?(NimbleCSV, :define, 2)
    refute macro_exported?(NimbleCSV, :define, 2)

    # It really is callable at runtime: nothing here is compile-time magic.
    refute Code.ensure_loaded?(RuntimeCSV)

    first =
      ExUnit.CaptureIO.capture_io(:stderr, fn ->
        NimbleCSV.define(RuntimeCSV, separator: "|", escape: "\"")
      end)

    assert first == ""
    assert Code.ensure_loaded?(RuntimeCSV)

    # apply/3 rather than RuntimeCSV.parse_string/2 on purpose. A direct call
    # would make the compiler warn "module RuntimeCSV is not available or is
    # yet to be defined", because at compile time of THIS file the module does
    # not exist yet. That warning is the actual reason define/2 belongs at the
    # top of a file: not that a runtime call fails, but that every caller
    # compiled before the call is compiled against a module that is missing.
    assert apply(RuntimeCSV, :parse_string, ["a|b", [skip_headers: false]]) == [["a", "b"]]

    # The real cost of putting define/2 inside a function: each invocation
    # re-runs defmodule, so the second one purges and replaces the first.
    second =
      ExUnit.CaptureIO.capture_io(:stderr, fn ->
        NimbleCSV.define(RuntimeCSV, separator: "|", escape: "\"")
      end)

    assert second =~ "redefining module RuntimeCSV"
  end

  # define/2 takes an already-resolved module atom, so the caller's alias is
  # expanded normally. The automatic nesting that `defmodule Inner` gets when
  # written inside `defmodule Outer` is a feature of the defmodule macro
  # applied to its own literal argument, and it does not reach through a
  # function call. lib/csx_nimble_csv/parsers.ex writes `NimbleCSV.define(
  # LooseCSV, ...)` inside `defmodule CsxNimbleCsv.Parsers`, and the parser
  # lands at the top level.
  test "a bare alias passed to define/2 is not nested under the enclosing module" do
    assert Code.ensure_loaded?(LooseCSV)
    refute Code.ensure_loaded?(CsxNimbleCsv.Parsers.LooseCSV)
    assert LooseCSV.parse_string("h\n'a,b',c", skip_headers: false) == [["h"], ["a,b", "c"]]
  end

  test "a defined parser round-trips a table, and only the first separator is used for dumping" do
    table = [["id", "name"], ["1", "Doe, Jane"], ["2", "x;y"]]

    dumped = table |> SemicolonCSV.dump_to_iodata() |> IO.iodata_to_binary()

    # separator: [";", ","] means BOTH are accepted when parsing but only ";"
    # is written when dumping. It also means both are "reserved", which is why
    # "Doe, Jane" comes back quoted even though this parser never writes a
    # comma as a separator — :reserved defaults to escape + line_separator +
    # every separator + every newline.
    assert dumped == ~s(id;name\n1;"Doe, Jane"\n2;"x;y"\n)
    assert SemicolonCSV.parse_string(dumped, skip_headers: false) == table

    # The second separator is live on the parsing side: this row is comma
    # separated and still splits.
    assert SemicolonCSV.parse_string("h1;h2\nr1a;r1b\nr2a,r2b", skip_headers: false) ==
             [["h1", "h2"], ["r1a", "r1b"], ["r2a", "r2b"]]
  end

  test "RFC4180 keeps the separator and the newline that sit inside a quoted field" do
    crlf = ~s(name,notes\r\n"Doe, Jane","line1\r\nline2"\r\n)

    # Two things at once: the comma inside the quotes does not split a field,
    # and the newline inside the quotes does not end the row even though
    # parse_string chops the input on newlines before parsing it. The bytes of
    # that inner newline are returned verbatim — a CRLF source yields "\r\n"
    # in the field, NOT a normalised "\n". Anything comparing against "\n"
    # here breaks on Windows-authored files.
    assert CSV.parse_string(crlf) == [["Doe, Jane", "line1\r\nline2"]]

    lf = ~s(name,notes\n"Doe, Jane","line1\nline2"\n)
    assert CSV.parse_string(lf) == [["Doe, Jane", "line1\nline2"]]

    # A doubled escape inside a quoted field is one literal quote.
    assert CSV.parse_string(~s(h\n"say ""hi"""), skip_headers: false) == [["h"], [~s(say "hi")]]
  end

  test "parse_string drops the first line by default and skip_headers: false keeps it" do
    assert CSV.parse_string("a,b\nc,d") == [["c", "d"]]
    assert CSV.parse_string("a,b\nc,d", skip_headers: false) == [["a", "b"], ["c", "d"]]

    # :skip_headers does not detect a header, it unconditionally discards the
    # first line. Feed a headerless single-row CSV to the default and the only
    # row you have is silently gone — an empty list, not an error. This is the
    # bug that shows up as "my last record is missing" on files assembled from
    # several sources.
    assert CSV.parse_string("a,b") == []
    assert CSV.parse_string("a,b", skip_headers: false) == [["a", "b"]]
  end

  test "broken escaping raises NimbleCSV.ParseError, but a ragged row does not" do
    # MEASURED, and it refutes the usual meaning of "malformed row".
    # nimble_csv performs no column-count validation whatsoever. Rows of
    # different widths parse happily and come back as lists of different
    # lengths, so a truncated line is NOT an error you can catch — it is data
    # you have to check yourself.
    assert CSV.parse_string("a,b,c\n1,2\n3,4,5,6", skip_headers: false) ==
             [["a", "b", "c"], ["1", "2"], ["3", "4", "5", "6"]]

    # What actually raises is broken escaping, and there are exactly two
    # messages. An unterminated quote is only detected once the input runs
    # out, because until then the parser is still legitimately accumulating a
    # multi-line field.
    assert_raise NimbleCSV.ParseError,
                 ~s(expected escape character " but reached the end of file),
                 fn -> CSV.parse_string(~s(h\n"unterminated), skip_headers: false) end

    # A quote in a position where the grammar cannot allow one. The message
    # includes the offending line, which is the only positional information
    # you get — there is no row or column number.
    assert_raise NimbleCSV.ParseError, ~s(unexpected escape character " in "a,b\\"c"), fn ->
      CSV.parse_string(~s(h\na,b"c), skip_headers: false)
    end

    assert_raise NimbleCSV.ParseError, ~s(unexpected escape character " in "a\\"b,c"), fn ->
      CSV.parse_string(~s(h\n"a"b,c), skip_headers: false)
    end
  end

  test "parse_string never yields partial data, but parse_stream does" do
    lines = ["ok1,a\n", "ok2,b\n", ~s("unterminated\n)]

    # parse_string and parse_enumerable are eager, so the ParseError happens
    # before any row reaches you: all-or-nothing. parse_string splits its
    # input on newlines and hands the pieces to parse_enumerable, so the two
    # share the failure model.
    assert_raise NimbleCSV.ParseError, fn -> CSV.parse_enumerable(lines, skip_headers: false) end

    assert_raise NimbleCSV.ParseError, fn ->
      CSV.parse_string(Enum.join(lines), skip_headers: false)
    end

    # parse_stream is lazy, and that changes the failure model completely.
    # Draining the stream raises...
    assert_raise NimbleCSV.ParseError, fn ->
      lines |> CSV.parse_stream(skip_headers: false) |> Enum.to_list()
    end

    # ...but stopping before the broken line returns the good rows and raises
    # nothing at all. A Stream.take, a limit, or an early halt will quietly
    # hand you a truncated result set from a file that is not valid CSV.
    assert lines |> CSV.parse_stream(skip_headers: false) |> Enum.take(2) ==
             [["ok1", "a"], ["ok2", "b"]]
  end

  test "dump_to_iodata returns iodata, not a binary" do
    iodata = CSV.dump_to_iodata([["a", "b"]])

    # The name is literal. This is a nested list holding binaries AND raw
    # integer bytes (44 is the comma separator, 34 is the quote), which is why
    # you cannot pattern match, String.split, or == it against a string.
    refute is_binary(iodata)
    assert is_list(iodata)
    assert List.flatten(iodata) == ["a", 44, "b", "\r\n"]
    assert Enum.any?(List.flatten(iodata), &is_integer/1)

    # The escape is a raw byte too, on both sides of a quoted field. A
    # single-byte setting becomes an integer in the iodata; the two-byte
    # "\r\n" row terminator above stays a binary because it does not fit in
    # one byte.
    assert List.flatten(CSV.dump_to_iodata([["Doe, Jane"]])) == [34, "Doe, Jane", 34, "\r\n"]

    # iodata is what IO.write/File.write want, so the conversion is usually
    # unnecessary; :erlang.iolist_size/1 gives the byte count without building
    # the binary.
    assert :erlang.iolist_size(iodata) == 5
    assert IO.iodata_to_binary(iodata) == "a,b\r\n"

    # RFC4180 dumps CRLF, because the spec says so — line_separator: "\r\n"
    # is baked into nimble_csv's own definition of this parser, overriding the
    # library default of "\n" that the parser in lib/ keeps. Parsing accepts
    # both line endings either way;
    # only dumping differs, so a round trip through RFC4180 changes your line
    # endings even when the file you read used bare "\n".
    assert IO.iodata_to_binary(CSV.dump_to_iodata([["a", "b"], ["c", "d"]])) == "a,b\r\nc,d\r\n"
    assert IO.iodata_to_binary(SemicolonCSV.dump_to_iodata([["a", "b"]])) == "a;b\n"

    # Fields carrying a reserved character get quoted on the way out, and the
    # embedded newline stays a bare "\n" inside the quotes while the row
    # terminator is "\r\n".
    table = [["name", "notes"], ["Doe, Jane", "line1\nline2"]]
    dumped = table |> CSV.dump_to_iodata() |> IO.iodata_to_binary()
    assert dumped == ~s(name,notes\r\n"Doe, Jane","line1\nline2"\r\n)
    assert CSV.parse_string(dumped, skip_headers: false) == table

    # dump runs each entry through to_string/1, so non-binaries are accepted.
    # There is no matching conversion when parsing: the integer and the atom
    # come back as binaries, so a dump/parse round trip is not identity for
    # anything that was not a string to begin with.
    assert IO.iodata_to_binary(CSV.dump_to_iodata([[1, :ok]])) == "1,ok\r\n"
    assert CSV.parse_string("1,ok\r\n", skip_headers: false) == [["1", "ok"]]
  end
end
