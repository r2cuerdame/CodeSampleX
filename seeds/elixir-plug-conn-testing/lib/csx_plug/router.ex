defmodule CsxPlug.Router do
  @moduledoc """
  A complete Plug.Router: parsed bodies and a 404 for anything unmatched.

  Compare with `CsxPlug.RawRouter`, which leaves both out.
  """

  use Plug.Router

  plug :match

  # Plug.Parsers is what turns a request body into conn.body_params. It is
  # not automatic and it is not part of Plug.Router — without this entry the
  # body stays unread no matter how correct the content-type is.
  #
  # :pass is the list of content types allowed through UNPARSED, and it is
  # consulted only after every parser in :parsers has declined. Listing
  # application/json here would be dead config for that reason — the :json
  # parser claims the type first — even though that is the spelling people
  # reach for. text/plain is listed instead so the contract can measure both
  # halves: what a passed type gets, and what a type in neither list gets.
  # Real configs mostly end up at pass: ["*/*"], which disables the second
  # half entirely.
  plug Plug.Parsers,
    parsers: [:json],
    pass: ["text/plain"],
    json_decoder: Jason

  plug :dispatch

  get "/hello" do
    send_resp(conn, 200, "hello")
  end

  get "/greet/:name" do
    send_resp(conn, 200, "hello " <> name)
  end

  post "/echo" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(conn.body_params))
  end

  # `match _` is the only reason an unknown path answers 404 instead of
  # raising. Plug.Router generates a function clause per route and nothing
  # else, so a router without this clause has no way to fail politely.
  match _ do
    send_resp(conn, 404, "no route")
  end
end
