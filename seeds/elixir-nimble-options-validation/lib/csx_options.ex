defmodule CsxOptions do
  @moduledoc """
  A library-style option schema built with NimbleOptions.

  This is the shape almost every Dashbit-adjacent library uses: one schema
  attribute, compiled once, wrapped in a `new/1` that returns a tuple and a
  `new!/1` that raises. The contract measures what a caller of this module
  actually sees when the options are wrong.
  """

  @schema [
    url: [
      type: :string,
      required: true,
      doc: "Base URL of the service."
    ],
    retries: [
      type: :non_neg_integer,
      default: 0,
      doc: "How many times to retry a failed request."
    ],
    mode: [
      type: {:in, [:sync, :async]},
      default: :sync,
      doc: "Whether a call blocks until the response arrives."
    ],
    pool: [
      type: :keyword_list,
      default: [],
      keys: [
        size: [type: :pos_integer, default: 10],
        overflow: [type: :boolean, default: false]
      ],
      doc: "Connection pool settings."
    ]
  ]

  # new!/1 validates the SCHEMA, not the options, and is the only part of this
  # worth doing at compile time. Handing the plain keyword list to validate/2
  # works too, but re-checks the schema on every call — measured: an unknown
  # type raises the identical ArgumentError through either path, so new!/1 buys
  # speed, not extra safety.
  #
  # What new!/1 does NOT check is that a :default satisfies its own :type. The
  # library's own option docs claim a default "is *validated* according to the
  # given `:type`", and that "you cannot have, for example, `type: :integer`
  # and use `default: "a string"`". Measured on 1.1.1: you can. new!/1 accepts
  # that schema without complaint, because the default is only validated at the
  # moment it is applied — so the schema is fine for every caller who passes
  # the option and broken for every caller who omits it. The contract pins it.
  @compiled NimbleOptions.new!(@schema)

  @doc """
  Validate options, returning `{:ok, normalized}` or
  `{:error, %NimbleOptions.ValidationError{}}`.

  The normalized list is a keyword list with defaults filled in. Its key ORDER
  is an implementation detail — see the contract — so read it with
  `Keyword.get/2` or `opts[:key]`, never by position or pattern match.
  """
  def new(opts), do: NimbleOptions.validate(opts, @compiled)

  @doc """
  Same as `new/1` but raises `NimbleOptions.ValidationError`, and returns the
  bare normalized list rather than an `:ok` tuple.
  """
  def new!(opts), do: NimbleOptions.validate!(opts, @compiled)

  @doc "The compiled schema, so callers can validate against it themselves."
  def schema, do: @compiled

  @doc "Markdown for the schema, meant to be interpolated into a @moduledoc."
  def docs, do: NimbleOptions.docs(@compiled)
end
