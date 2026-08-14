defmodule CsxPlugTest do
  use ExUnit.Case

  # Plug.Test replaces the server, not the request. conn/3 builds a real
  # %Plug.Conn{} whose adapter is a {module, state} tuple living in this
  # process, and calling the router is a plain function call — no port is
  # bound and no socket is opened, which is why this file runs with the
  # network switched off.
  #
  # `use Plug.Test` was deprecated in plug 1.17. The replacement is the two
  # imports below, and Plug.Conn is the half people leave out: conn/3 and
  # sent_resp/1 come from Plug.Test, but put_req_header, get_resp_header,
  # send_resp and put_resp_content_type all live in Plug.Conn.
  import Plug.Test
  import Plug.Conn

  alias CsxPlug.RawRouter
  alias CsxPlug.Router

  # A plug is init/1 then call/2. Nothing hidden happens in between, which is
  # the whole reason this style of test works.
  defp call(router, conn), do: router.call(conn, router.init([]))

  defp json_post(path, body) do
    conn(:post, path, body) |> put_req_header("content-type", "application/json")
  end

  test "the conn is a struct in this process and nothing is listening" do
    c = conn(:get, "/hello")
    assert {Plug.Adapters.Test.Conn, payload} = c.adapter

    # The owning PID is inside the adapter payload, not on conn.owner. That
    # field is listed under "Deprecated fields" in Plug.Conn as of plug 1.17,
    # because tracking the response moved into the adapters, and the test
    # adapter never fills it. Bandit does not fill it either — it keeps its
    # own owner_pid in adapter state, exactly like this one does — while
    # Plug.Cowboy still sets it. An assertion on conn.owner is therefore an
    # assertion about which adapter you happen to be running.
    assert payload.owner == self()
    assert c.owner == nil

    # There is no socket behind any of this. The peer and host values are
    # fabricated loopback defaults, which is what makes the suite runnable
    # with no network at all.
    assert c.remote_ip == {127, 0, 0, 1}
    assert payload.peer_data.address == {127, 0, 0, 1}
    assert c.host == "www.example.com"
    assert c.port == 80
    assert c.scheme == :http
  end

  test "status is nil before the plug runs and set after" do
    conn = conn(:get, "/hello")
    assert conn.status == nil
    assert conn.state == :unset
    assert conn.resp_body == nil

    sent = call(Router, conn)
    assert sent.status == 200
    assert sent.state == :sent
    assert sent.resp_body == "hello"

    # sent_resp/1 receives the message the test adapter sent to this process
    # when the response went out, so it only works from the process that built
    # the conn. It is the assertion to reach for when you want to prove the
    # response was actually sent rather than merely assembled — conn.resp_body
    # is set by resp/3 too, and resp/3 sends nothing.
    assert {200, _headers, "hello"} = sent_resp(sent)

    staged = resp(conn(:get, "/hello"), 201, "staged")
    assert staged.resp_body == "staged"
    assert staged.state == :set
    assert_raise RuntimeError, fn -> sent_resp(staged) end
  end

  test "path segments arrive as bound variables in the route body" do
    assert call(Router, conn(:get, "/greet/world")).resp_body == "hello world"
  end

  test "an unmatched path is a 404 only because match _ exists" do
    assert call(Router, conn(:get, "/nope")).status == 404
    assert call(Router, conn(:get, "/nope")).resp_body == "no route"

    # RawRouter has no catch-all. Plug.Router compiles one function clause
    # per route and adds no fallback, so an unknown path is a
    # FunctionClauseError rather than a status. This is the trap: 404 looks
    # like a framework default until the first unknown path arrives.
    assert_raise FunctionClauseError, fn -> call(RawRouter, conn(:get, "/nope")) end
  end

  test "the method is part of the match, not just the path" do
    # /hello exists as a GET. A POST to it falls through to match _, so the
    # answer is 404 and not 405.
    assert call(Router, json_post("/hello", ~s({"a": 1}))).status == 404
  end

  test "Plug.Parsers is what puts a JSON body in body_params" do
    sent = call(Router, json_post("/echo", ~s({"a": 1, "b": "two"})))
    assert sent.status == 200
    assert sent.body_params == %{"a" => 1, "b" => "two"}
    assert Jason.decode!(sent.resp_body) == %{"a" => 1, "b" => "two"}
  end

  test "params merges the query string, body_params does not" do
    sent = call(Router, json_post("/echo?q=1", ~s({"a": 2})))
    assert sent.body_params == %{"a" => 2}
    assert sent.params == %{"a" => 2, "q" => "1"}
    assert sent.query_params == %{"q" => "1"}
  end

  test "a top-level JSON array is filed under _json" do
    # body_params is always a map, so an array body has to go somewhere. Plug
    # picks the "_json" key, and code that expects to match the list directly
    # is what breaks.
    sent = call(Router, json_post("/echo", ~s([1, 2, 3])))
    assert sent.body_params == %{"_json" => [1, 2, 3]}
  end

  test "without Plug.Parsers the body never becomes body_params" do
    built = json_post("/echo", ~s({"a": 1}))
    assert %Plug.Conn.Unfetched{aspect: :body_params} = built.body_params

    # On a conn that has not been through a router, reading a param raises,
    # and the message names the plug that is missing.
    err = assert_raise ArgumentError, fn -> built.params["a"] end
    assert Exception.message(err) =~ "Configure and invoke Plug.Parsers"

    sent = call(RawRouter, built)
    assert sent.status == 200
    assert sent.resp_body == "%Plug.Conn.Unfetched{aspect: :body_params}"
    assert %Plug.Conn.Unfetched{aspect: :body_params} = sent.body_params

    # The raise above does not survive the router, though. Plug.Router's
    # :match step replaces conn.params with the route's path params, so on a
    # router the missing parser reads back as nil instead of raising. That is
    # the failure mode people actually meet: not an exception naming
    # Plug.Parsers, just a parameter that is quietly absent.
    assert sent.params == %{}
    assert sent.params["a"] == nil
  end

  test "an unparsed content type is passed or raised depending on :pass" do
    # text/plain is in :pass, so no parser touches the body and the request
    # still reaches a route. What it does not get is an empty map: body_params
    # keeps the Unfetched placeholder, so "the content type was allowed" and
    # "the body was parsed" are two different things. This goes to the
    # catch-all rather than /echo only because /echo would try to JSON-encode
    # that placeholder.
    passed = conn(:post, "/nope", "plain body") |> put_req_header("content-type", "text/plain")
    sent = call(Router, passed)
    assert sent.status == 404
    assert %Plug.Conn.Unfetched{aspect: :body_params} = sent.body_params

    # application/xml is in neither list. Plug.Parsers raises rather than
    # handing the route an empty body, which is the behaviour pass: ["*/*"]
    # throws away.
    xml = conn(:post, "/echo", "<a/>") |> put_req_header("content-type", "application/xml")
    assert_raise Plug.Parsers.UnsupportedMediaTypeError, fn -> call(Router, xml) end
  end

  test "handing conn/3 a map skips the parser instead of exercising it" do
    # This is the mistake worth knowing about. A map or keyword body does not
    # get encoded as JSON: Plug.Test writes it straight into body_params —
    # keys stringified — and labels the request multipart/mixed.
    mapped = conn(:post, "/echo", %{a: 1})
    assert get_req_header(mapped, "content-type") == ["multipart/mixed; boundary=plug_conn_test"]
    assert mapped.body_params == %{"a" => 1}

    # Plug.Parsers only reaches its parser list while body_params is still
    # unfetched. Here it is already a map, so the parsers — and with them the
    # content-type check — are skipped: no UnsupportedMediaTypeError for a
    # multipart/mixed it cannot parse, a 200 either way, and therefore no
    # evidence that the parser configuration works at all. The plug itself
    # still runs; it just merges params instead of parsing anything.
    sent = call(Router, mapped)
    assert sent.status == 200
    assert sent.body_params == %{"a" => 1}

    # And the raw body is a placeholder rather than the encoded map, so
    # anything reading the body itself sees this. A binary body is the only
    # form that puts the real bytes through the parser.
    assert {:ok, "--plug_conn_test--", _} = read_body(conn(:post, "/echo", %{a: 1}))
    assert {:ok, ~s({"a": 1}), _} = read_body(json_post("/echo", ~s({"a": 1})))
  end

  test "send_resp twice raises Plug.Conn.AlreadySentError" do
    sent = call(Router, conn(:get, "/hello"))
    assert_raise Plug.Conn.AlreadySentError, fn -> send_resp(sent, 200, "again") end
    # Everything that would change the response is gated the same way, so a
    # header cannot be bolted on after the fact either.
    assert_raise Plug.Conn.AlreadySentError, fn -> put_resp_header(sent, "x-late", "1") end
  end

  test "put_resp_content_type appends a charset and stores the name lowercased" do
    sent = call(Router, json_post("/echo", ~s({"a": 1})))

    # The stored value is not the one that was passed in:
    # put_resp_content_type/2 adds charset=utf-8, so an equality assertion
    # against "application/json" fails.
    assert get_resp_header(sent, "content-type") == ["application/json; charset=utf-8"]

    # Header names in Plug are lowercase, and get_resp_header/2 compares them
    # exactly rather than case-insensitively, so the capitalised spelling
    # returns [] and an assertion written that way can never fail.
    assert get_resp_header(sent, "Content-Type") == []

    # The third argument is the charset, and nil suppresses it.
    bare = conn(:get, "/x") |> put_resp_content_type("application/json", nil)
    assert get_resp_header(bare, "content-type") == ["application/json"]
  end
end
