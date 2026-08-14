defmodule CsxEcto.Signup do
  @moduledoc """
  Ecto.Changeset used as a standalone validator for a plain map: an
  embedded_schema, no Repo, no ecto_sql, no database of any kind.

  embedded_schema is the piece that makes this work. It defines the same
  struct and the same `__schema__/1` reflection a table-backed schema gets,
  minus the table, so every cast and validate function behaves identically
  while nothing ever opens a connection. The only Ecto function in the whole
  library that needs a Repo is the one that talks to a Repo.
  """

  use Ecto.Schema
  import Ecto.Changeset

  # embedded_schema defaults to @primary_key {:id, :binary_id, autogenerate: true}.
  # Autogeneration happens inside a Repo insert, so with no Repo that field
  # would be a permanent nil pretending to be an id. Turning it off keeps the
  # struct honest about what it is.
  @primary_key false
  embedded_schema do
    field :name, :string
    field :email, :string
    field :age, :integer
    field :accepted_terms, :boolean

    # :role is a real field that is deliberately missing from @permitted.
    # This is the security-relevant half of cast/4 and the reason the
    # permitted list exists at all: a request that sends "role" => "admin"
    # gets no error, no change, and no warning. See changeset/1.
    field :role, :string, default: "member"
  end

  @permitted [:name, :email, :age, :accepted_terms]

  @doc """
  Build a changeset from raw string-keyed params, the shape a JSON body
  arrives in.

  cast/4 does three things at once, and conflating them is where the
  surprises come from: it filters params down to the permitted list, it
  converts each remaining value to the field's declared type, and it replaces
  a value it considers empty with the field's declared default before storing
  anything. Only the second of those can produce an error. Filtering is
  silent, and emptying looks like the param was never sent. The replacement is
  the declared default and not nil, which only becomes visible once a field
  has one.
  """
  def changeset(params) when is_map(params) do
    %__MODULE__{}
    |> cast(params, @permitted)
    |> validate_required([:name, :email])
    |> validate_format(:email, ~r/^[^@\s]+@[^@\s]+$/)
    |> validate_number(:age, greater_than_or_equal_to: 18)
  end

  @doc """
  Render `changeset.errors` as the `%{field => [message]}` map an API
  response wants.

  Ecto stores an error as `{message, opts}` where the message still contains
  `%{placeholder}` markers and the opts carry the values, so that a
  translation layer can pick a different string per locale and per count.
  That means the raw message is not displayable: printing
  `elem(error, 0)` puts a literal "%{number}" in front of a user. Something
  has to do this substitution, and outside Phoenix nothing does it for you.
  """
  def error_map(%Ecto.Changeset{} = changeset) do
    Ecto.Changeset.traverse_errors(changeset, fn {message, opts} ->
      Regex.replace(~r"%{(\w+)}", message, fn _match, key ->
        opts |> Keyword.get(String.to_existing_atom(key), key) |> to_string()
      end)
    end)
  end
end
