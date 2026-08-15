defmodule CsxOptionsTest do
  use ExUnit.Case

  # NimbleOptions is the schema validator underneath Broadway, Nx, Req, Oban
  # and most of Dashbit's libraries, so its error strings are what THEIR users
  # read when they mistype an option. That is why this file asserts the exact
  # messages rather than just the error shape: the message is the product.
  #
  # Everything below is measured against nimble_options 1.1.1 on Elixir 1.20.
  # Three of the assertions contradict what the library's own docs say or what
  # the schema keyword reads like. The measurement is kept and named.

  alias NimbleOptions.ValidationError

  @url "https://example.com"

  test "a valid list comes back normalized with defaults filled, in an order you must not rely on" do
    {:ok, opts} = CsxOptions.new(url: @url)

    assert opts[:url] == @url
    assert opts[:retries] == 0
    assert opts[:mode] == :sync

    # The parent default is [], and the nested schema is applied to that
    # default, so omitting :pool entirely still yields both nested defaults.
    assert opts[:pool] == [overflow: false, size: 10]

    # The order. Defaults are inserted with Keyword.put/3, which PREPENDS, and
    # the schema is walked front to back — so the filled-in defaults come out
    # in reverse schema order, ahead of the key the caller actually passed.
    # Nothing documents this and nothing guarantees it, which is the point:
    # read the result with opts[:key], never by position or pattern match.
    assert opts == [pool: [overflow: false, size: 10], mode: :sync, retries: 0, url: @url]

    # Supply every key and no default is inserted, so the caller's own order
    # survives untouched. The two shapes differing is what makes an
    # order-dependent match pass in tests and fail in production.
    {:ok, full} = CsxOptions.new(url: @url, retries: 3, mode: :async, pool: [size: 2])
    assert full == [url: @url, retries: 3, mode: :async, pool: [overflow: false, size: 2]]
  end

  test "an unknown key is rejected with the valid keys listed in schema order" do
    {:error, error} = CsxOptions.new(url: @url, retires: 3)

    assert error.message ==
             "unknown options [:retires], valid options are: [:url, :retries, :mode, :pool]"

    # :key holds a LIST here, not an atom, even though the ValidationError
    # struct docs type the field as atom(). Only the unknown-options branch
    # does this; every other error puts a single key in it. Code that does
    # `Atom.to_string(error.key)` in a generic error handler dies right here.
    assert error.key == [:retires]
    assert error.value == nil
    assert error.keys_path == []

    # Unknown keys are checked before required ones, so a typo hides a
    # genuinely missing :url rather than being reported alongside it.
    {:error, only_bogus} = CsxOptions.new(bogus: 1)

    assert only_bogus.message ==
             "unknown options [:bogus], valid options are: [:url, :retries, :mode, :pool]"
  end

  test "a duplicated key is reported as unknown, in a message that lists it as valid" do
    # Written with ++ so this stays a runtime list rather than a literal the
    # compiler might fold or warn about.
    duplicated = [url: @url] ++ [url: "https://example.com/other"]

    {:error, error} = CsxOptions.new(duplicated)

    # The check is `Keyword.keys(opts) -- Keyword.keys(schema)`, and --
    # removes only ONE occurrence per element. Two :url entries therefore
    # leave one behind and it is reported as unknown, producing a message
    # that names :url in both halves. Keyword lists allow duplicates, so
    # config merged from two places hits this and the message misdirects.
    assert error.message ==
             "unknown options [:url], valid options are: [:url, :retries, :mode, :pool]"

    assert error.key == [:url]
  end

  test "a type mismatch names the key and spells out the expected type" do
    {:error, error} = CsxOptions.new(url: @url, retries: -1)

    assert Exception.message(error) ==
             "invalid value for :retries option: expected non negative integer, got: -1"

    assert error.key == :retries
    assert error.value == -1

    {:error, wrong_string} = CsxOptions.new(url: :not_a_string)

    assert Exception.message(wrong_string) ==
             "invalid value for :url option: expected string, got: :not_a_string"

    # {:in, choices} renders the choices themselves, which is why it is the
    # right type for an enum: the message tells the caller what to write.
    {:error, wrong_choice} = CsxOptions.new(url: @url, mode: :maybe)

    assert Exception.message(wrong_choice) ==
             "invalid value for :mode option: expected one of [:sync, :async], got: :maybe"

    # The wording is not consistent between types and there is no way to
    # override it: :non_neg_integer says "non negative" with a space, while
    # :timeout says "non-negative" with a hyphen. Do not grep these messages.
    {:error, timeout} = NimbleOptions.validate([t: -1], t: [type: :timeout])

    assert Exception.message(timeout) ==
             "invalid value for :t option: expected non-negative integer or :infinity, got: -1"
  end

  test "required means required, and required: true together with a default is a dead default" do
    {:error, error} = CsxOptions.new(retries: 1)

    assert Exception.message(error) == "required :url option not found, received options: [:retries]"
    assert error.key == :url
    assert error.value == nil

    # "received options" lists the caller's ORIGINAL keys, not the working
    # list that defaults have already been added to. :first below carries a
    # default and precedes the required :second in schema order, so its
    # default IS inserted before the required check fails — and still does not
    # show up. The message reports what the caller typed, not what the library
    # did to it.
    ordered = [first: [type: :integer, default: 1], second: [type: :atom, required: true]]
    {:error, orig_keys} = NimbleOptions.validate([], ordered)

    assert Exception.message(orig_keys) ==
             "required :second option not found, received options: []"

    # What makes a key optional is :required defaulting to false, NOT the
    # presence of a :default — a key with neither validates fine and is simply
    # absent from the result. The one thing a :default adds is that the key is
    # guaranteed PRESENT afterwards, so callers can read it without a fallback.
    assert NimbleOptions.validate([], a: [type: :integer]) == {:ok, []}
    assert NimbleOptions.validate([], a: [type: :integer, default: 7]) == {:ok, [a: 7]}

    # In this schema only :url is demanded; :retries, :mode and :pool all pass
    # unspecified.
    assert {:ok, _} = CsxOptions.new(url: @url)

    # But writing both on ONE key does not mean "required, and here is the
    # fallback". Measured: the required check runs first and returns before
    # the default is ever applied, so the default is unreachable and the
    # caller gets an error naming a key you thought you had covered.
    # new!/1 accepts the schema silently — there is no warning for this.
    both = [level: [type: :atom, required: true, default: :info]]
    assert %NimbleOptions{} = NimbleOptions.new!(both)

    {:error, dead_default} = NimbleOptions.validate([], both)
    assert Exception.message(dead_default) == "required :level option not found, received options: []"

    # The default only ever applies when the key is absent, and absent is
    # exactly the case required: true rejects. Passing the key works, which
    # is why this survives a test suite that always passes the option.
    assert NimbleOptions.validate([level: :warn], both) == {:ok, [level: :warn]}
  end

  test "a default that violates its own type is accepted by new!/1 and only fails when applied" do
    # The :default option's own documentation says the value "is *validated*
    # according to the given `:type`" and that "you cannot have, for example,
    # `type: :integer` and use `default: \"a string\"`". Measured on 1.1.1:
    # you can. Schema compilation accepts it.
    bad = [a: [type: :integer, default: "not an integer"]]
    assert %NimbleOptions{} = NimbleOptions.new!(bad)

    # The default is validated, but lazily — at the moment it is inserted.
    # So the schema is broken only for callers who OMIT the option.
    {:error, error} = NimbleOptions.validate([], bad)

    assert Exception.message(error) ==
             "invalid value for :a option: expected integer, got: \"not an integer\""

    assert NimbleOptions.validate([a: 1], bad) == {:ok, [a: 1]}

    # A type that does not exist, by contrast, IS caught at schema level, and
    # as an ArgumentError rather than a ValidationError.
    assert_raise ArgumentError, ~r/unknown type :intiger/, fn ->
      NimbleOptions.new!(a: [type: :intiger])
    end

    # Both paths raise the identical ArgumentError, because validate/2 given a
    # raw keyword-list schema calls new!/1 on it internally. Compiling the
    # schema up front therefore buys speed, not extra checking.
    assert_raise ArgumentError, ~r/unknown type :intiger/, fn ->
      NimbleOptions.validate([a: 1], a: [type: :intiger])
    end
  end

  test "nested :keyword_list schemas validate, and the path lives outside the message field" do
    {:error, error} = CsxOptions.new(url: @url, pool: [size: 0])

    # The :message FIELD is the bare message. The path to the nested key is a
    # separate field, and only Exception.message/1 joins the two. Reading
    # error.message directly — the obvious thing to do with a struct — silently
    # drops the one piece of information that says WHERE the bad key was.
    assert error.message == "invalid value for :size option: expected positive integer, got: 0"
    assert error.keys_path == [:pool]
    assert error.key == :size

    assert Exception.message(error) ==
             "invalid value for :size option: expected positive integer, got: 0 (in options [:pool])"

    {:error, nested_unknown} = CsxOptions.new(url: @url, pool: [sizee: 1])

    assert Exception.message(nested_unknown) ==
             "unknown options [:sizee], valid options are: [:size, :overflow] (in options [:pool])"

    # A required key inside a nested schema is only reached when the parent is
    # present. Give the parent no default and omitting it skips the nested
    # schema entirely, so :host is required only conditionally — the reason
    # CsxOptions gives :pool a default of [] instead of leaving it out.
    conn = [
      conn: [
        type: :keyword_list,
        keys: [
          host: [type: :string, required: true],
          port: [type: :pos_integer, default: 80]
        ]
      ]
    ]

    {:error, missing_host} = NimbleOptions.validate([conn: [port: 1]], conn)

    assert Exception.message(missing_host) ==
             "required :host option not found, received options: [:port] (in options [:conn])"

    assert NimbleOptions.validate([], conn) == {:ok, []}
  end

  test "validate returns a tuple and validate! raises the identical error" do
    assert {:error, %ValidationError{}} = CsxOptions.new(retries: 1)

    error =
      assert_raise ValidationError, fn ->
        CsxOptions.new!(retries: 1)
      end

    assert Exception.message(error) == "required :url option not found, received options: [:retries]"

    # validate!/2 returns the bare normalized list, NOT an {:ok, list} tuple.
    # Swapping validate for validate! therefore changes the success shape as
    # well as the failure mode.
    assert CsxOptions.new!(url: @url) == [
             pool: [overflow: false, size: 10],
             mode: :sync,
             retries: 0,
             url: @url
           ]
  end

  test "a map of options is only accepted against a compiled schema" do
    # Given a %NimbleOptions{}, a map goes in and a map comes out, defaults
    # included.
    assert {:ok, validated} = NimbleOptions.validate(%{url: @url}, CsxOptions.schema())
    assert validated == %{url: @url, retries: 0, mode: :sync, pool: [overflow: false, size: 10]}

    # Hand the same map to a raw keyword-list schema and there is no matching
    # clause: validate/2 guards `is_list(options)` on that branch, so map
    # support exists only through new!/1 even though the @spec on validate/2
    # reads `keyword() | map()`. apply/3 is used so this stays a runtime
    # assertion: written as a literal call, Elixir 1.20's type checker prints
    # an "incompatible types given to NimbleOptions.validate/2" diagnostic
    # naming both clauses. Measured — that diagnostic is a WARNING, not a
    # rejection: the module still compiles and still raises at run time.
    assert_raise FunctionClauseError, fn ->
      apply(NimbleOptions, :validate, [%{url: @url}, [url: [type: :string]]])
    end
  end

  test "docs/1 renders the schema, and omits the type for types it cannot name" do
    # This string is what ends up in a library's published documentation, so
    # it is worth pinning: :doc text precedes the default sentence, :required
    # renders as a bare "Required.", and {:in, choices} produces NO type link
    # at all — :mode is the only entry below without one.
    expected = """
    * `:url` (`t:String.t/0`) - Required. Base URL of the service.

    * `:retries` (`t:non_neg_integer/0`) - How many times to retry a failed request. The default value is `0`.

    * `:mode` - Whether a call blocks until the response arrives. The default value is `:sync`.

    * `:pool` (`t:keyword/0`) - Connection pool settings. The default value is `[]`.

      * `:size` (`t:pos_integer/0`) - The default value is `10`.

      * `:overflow` (`t:boolean/0`) - The default value is `false`.

    """

    assert CsxOptions.docs() == expected
  end
end
