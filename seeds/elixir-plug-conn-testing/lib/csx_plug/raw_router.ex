defmodule CsxPlug.RawRouter do
  @moduledoc """
  The same router with Plug.Parsers and the catch-all clause removed, so the
  contract can measure what each omission actually does rather than only
  asserting that the complete version works.
  """

  use Plug.Router

  # :match is not innocent here. It writes the route's path params over
  # conn.params, which replaces the Unfetched placeholder that would have
  # raised on a missing parser with an ordinary empty map. That is why a
  # router with no Plug.Parsers returns nil for a body parameter instead of
  # telling you which plug is missing.
  plug :match
  plug :dispatch

  # Echoes whatever conn.body_params holds. With no parser in the pipeline
  # that is not %{} — it is a placeholder struct, and inspecting it is the
  # fastest way to see the omission.
  post "/echo" do
    send_resp(conn, 200, inspect(conn.body_params))
  end
end
