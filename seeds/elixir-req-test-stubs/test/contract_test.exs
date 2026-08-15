defmodule CsxReqTest do
  # async: true is safe on purpose. Req.Test keeps stubs in an ownership
  # registry keyed by the calling process (vendored nimble_ownership, the
  # same model as Mox) and the registry starts in :private mode, so two
  # concurrent tests can register different plugs under the same name. That
  # is the one thing a mutable module-level response queue cannot do.
  use ExUnit.Case, async: true

  alias CsxReq.Weather

  @url "https://api.example.com/items"

  # Counts the :hit messages the stubs below send to this process. Req runs
  # the plug synchronously in the caller, so retries land in this mailbox.
  defp hits(acc \\ 0) do
    receive do
      :hit -> hits(acc + 1)
    after
      0 -> acc
    end
  end

  defp body(name, opts \\ []) do
    Req.get!([plug: {Req.Test, name}, url: @url, retry: false] ++ opts).body
  end

  ## 1. Decoding is driven by the response content-type

  test "a stub returns a body and the content-type decodes it, with no accessor call" do
    Req.Test.stub(Weather, fn conn -> Req.Test.json(conn, %{"celsius" => 25.0}) end)

    {:ok, resp} = Weather.get_temperature("Krakow")
    assert resp.status == 200

    # Req.Test.json/2 sets the content-type as a side effect; that header, not
    # the helper's name, is the whole mechanism. Headers are a map of lists in
    # Req 0.5+, not a keyword list, and a missing header reads as nil rather
    # than []. The cache-control entry is Plug.Conn's default, present on
    # every stubbed response.
    assert resp.headers == %{
             "cache-control" => ["max-age=0, private, must-revalidate"],
             "content-type" => ["application/json; charset=utf-8"]
           }

    assert Req.Response.get_header(resp, "content-type") == ["application/json; charset=utf-8"]
    assert Req.Response.get_header(resp, "x-missing") == []
    assert resp.headers["x-missing"] == nil

    # The body is already a map. There is no resp.json() step, which is what
    # makes production code able to pattern match straight on the body.
    assert resp.body == %{"celsius" => 25.0}
    assert Weather.get_rating("Krakow") == {:ok, :nice}
  end

  test "the same bytes without a content-type stay a binary and break the caller's match" do
    raw = Jason.encode!(%{"celsius" => 25.0})
    Req.Test.stub(Weather, fn conn -> Plug.Conn.send_resp(conn, 200, raw) end)

    {:ok, resp} = Weather.get_temperature("Krakow")
    assert resp.status == 200
    assert Req.Response.get_header(resp, "content-type") == []
    assert resp.body == raw

    # A perfectly good 200 that the caller cannot read: decode_body never ran,
    # so the %{"celsius" => _} clause does not match. Stub with
    # Req.Test.json/2, or Plug.Conn.put_resp_content_type/2, not send_resp/3
    # alone.
    assert Weather.get_rating("Krakow") == :error
  end

  test "with no content-type the URL path decides, so a .json path decodes anyway" do
    raw = Jason.encode!(%{"celsius" => 25.0})
    plug = fn conn -> Plug.Conn.send_resp(conn, 200, raw) end

    # Req.Steps.decode_body falls back to "application/octet-stream" when the
    # response has no content-type, and octet-stream is resolved through
    # MIME.from_path(request.url.path). Identical stub, identical bytes, and
    # the body type is decided by the request URL. This is the reason a stub
    # that "works" against one endpoint suddenly returns a string on another.
    assert Req.get!(plug: plug, url: "https://api.example.com/data.json").body ==
             %{"celsius" => 25.0}

    assert Req.get!(plug: plug, url: "https://api.example.com/data").body == raw
    assert Req.get!(plug: plug, url: "https://api.example.com/data.txt").body == raw
  end

  ## 2. A 500 is a value; retry is the step that reacts to it

  test "an HTTP 500 does not raise, not even from the bang functions" do
    Req.Test.stub(Weather, fn conn ->
      conn |> Plug.Conn.put_status(500) |> Req.Test.json(%{"error" => "boom"})
    end)

    # Req.request/1 returns :error only when the exchange did not happen.
    # A 500 happened, so it is {:ok, response}.
    {:ok, resp} = Weather.get_temperature("Krakow", retry: false)
    assert resp.status == 500
    # Error bodies go through the same decoder as success bodies.
    assert resp.body == %{"error" => "boom"}

    # Req.request!/1 raises the exception from {:error, exception}. There is
    # no exception here, so it returns the 500 response.
    assert %Req.Response{status: 500} =
             Req.request!(plug: {Req.Test, Weather}, url: @url, retry: false)

    # Raising on 4xx/5xx is opt-in. Note which body the message carries: the
    # undecoded binary, because the response steps run
    # retry -> handle_http_errors -> ... -> decode_body, so the raise happens
    # before the decoder. The exception you read in CI and the body the same
    # request would have returned are not the same value.
    error =
      assert_raise(RuntimeError, fn ->
        Weather.get_temperature("Krakow", retry: false, http_errors: :raise)
      end)

    assert Exception.message(error) ==
             "The requested URL returned error: 500\n" <>
               ~s|Response body: "{\\"error\\":\\"boom\\"}"|
  end

  test "retry fires on a 500 for GET but not for POST" do
    Req.Test.stub(RetryStub, fn conn ->
      send(self(), :hit)
      Plug.Conn.send_resp(conn, 500, "boom")
    end)

    opts = [plug: {Req.Test, RetryStub}, url: @url, retry_delay: 0, retry_log_level: false]

    # :retry defaults to :safe_transient: 408/429/500/502/503/504 and a few
    # transport errors, on GET and HEAD only. :max_retries defaults to 3, so a
    # single failing GET calls the stub four times. A stub is reusable, so
    # nothing here signals that three extra requests happened — count them.
    assert Req.get!(opts).status == 500
    assert hits() == 4

    # The same 500 on POST is returned immediately: retrying a non-idempotent
    # method is not safe, so :safe_transient excludes it.
    assert Req.post!(opts).status == 500
    assert hits() == 1

    # :transient is the opt-in for every method.
    assert Req.post!(opts ++ [retry: :transient]).status == 500
    assert hits() == 4
  end

  test "a stub can fail below HTTP, which a queue of responses cannot express" do
    Req.Test.stub(BrokenStub, &Req.Test.transport_error(&1, :timeout))

    opts = [plug: {Req.Test, BrokenStub}, url: @url]

    assert Req.get(opts ++ [retry: false]) == {:error, %Req.TransportError{reason: :timeout}}
    assert_raise Req.TransportError, fn -> Req.get!(opts ++ [retry: false]) end

    # :timeout is in the transient set, so this is also what retry reacts to.
    assert {:error, %Req.TransportError{}} =
             Req.get(opts ++ [retry_delay: 0, retry_log_level: false])

    assert hits() == 0
  end

  ## 3. expect versus stub

  test "expectations are consumed in order and then fall through to the stub" do
    Req.Test.stub(OrderStub, fn conn -> Plug.Conn.send_resp(conn, 200, "stub") end)
    Req.Test.expect(OrderStub, 2, fn conn -> Plug.Conn.send_resp(conn, 200, "expected") end)

    # expect/3 wins while it has uses left, regardless of registration order.
    assert body(OrderStub) == "expected"
    assert body(OrderStub) == "expected"

    # Once drained, Req.Test falls back to the stub instead of failing. So a
    # stub registered alongside expectations turns "used too many times" from
    # an error into silence — that combination is only worth it when the extra
    # calls genuinely do not matter.
    assert body(OrderStub) == "stub"
    assert Req.Test.verify!(OrderStub) == :ok
  end

  test "an exhausted expectation raises, and an unregistered name raises differently" do
    Req.Test.expect(OnceStub, fn conn -> Plug.Conn.send_resp(conn, 200, "once") end)
    assert body(OnceStub) == "once"

    # expect/3 without a stub is the strict form. expect/2 defaults to one
    # use, so the second request blows up at the call site rather than at
    # verification time.
    assert_raise RuntimeError, "no mock or stub for #{inspect(OnceStub)}", fn ->
      body(OnceStub)
    end

    # Different failure, different message: this one means the name was never
    # registered from a process this one can see. If you hit it in a spawned
    # task, the fix is Req.Test.allow/3, not another stub.
    assert_raise RuntimeError, ~r/^cannot find mock\/stub .* in process #PID/, fn ->
      body(UnregisteredStub)
    end
  end

  test "an unmet expectation is silent until something verifies it" do
    Req.Test.expect(UnmetStub, 2, fn conn -> Plug.Conn.send_resp(conn, 200, "ok") end)
    assert body(UnmetStub) == "ok"

    # Nothing has failed at this point, and nothing will: Req.Test does not
    # fail a test on its own. Asking is either verify!/0,1 here or
    # `setup {Req.Test, :verify_on_exit!}`, which is the version that also
    # covers tests that forget to ask.
    assert_raise RuntimeError, ~r/expected .* to be still used 1 more times/, fn ->
      Req.Test.verify!(UnmetStub)
    end

    # verify!/1 is scoped to one name; verify!/0 checks every name this
    # process owns, which matters once a test registers more than one.
    assert Req.Test.verify!(OtherStub) == :ok
  end

  ## 4. Turning the decoding off

  test "decoding can be switched off, and :decoders replaces the default list" do
    raw = ~s({"celsius":25.0})
    Req.Test.stub(DecodeStub, fn conn -> Req.Test.json(conn, %{"celsius" => 25.0}) end)

    assert body(DecodeStub) == %{"celsius" => 25.0}

    # Three separate switches, all landing on the undecoded binary. :raw also
    # turns off decompression; :decoders false turns off every format;
    # :decode_body false is the narrow one.
    assert body(DecodeStub, decode_body: false) == raw
    assert body(DecodeStub, decoders: false) == raw
    assert body(DecodeStub, raw: true) == raw

    # :decoders replaces the default [:json, :json_api] rather than adding to
    # it, so asking for one more format silently loses JSON.
    assert body(DecodeStub, decoders: [:gz]) == raw
    assert body(DecodeStub, decoders: [:gz, :json]) == %{"celsius" => 25.0}

    # req 0.7.0 deprecated `decode_json: opts` in favour of a codec under
    # :decoders. This is the replacement spelling for atom keys.
    assert body(DecodeStub, decoders: [json: &Jason.decode(&1, keys: :atoms)]) ==
             %{celsius: 25.0}
  end

  ## Wiring

  test "Req.Test is a plug and the stub name is its options" do
    # plug: {Req.Test, name} is the ordinary {module, options} plug form, so
    # Req.Plug calls Req.Test.call(conn, Req.Test.init(name)). The name is not
    # a config key Req looks up: it is literally the plug's options.
    assert Req.Test.__info__(:attributes)[:behaviour] == [Plug]
    assert Req.Test.init(SomeName) == SomeName

    # A bare function plug needs no registry at all. What Req.Test adds on top
    # is the name indirection and the process ownership, not the stubbing.
    plug = fn conn -> Req.Test.json(conn, %{"ok" => true}) end
    assert Req.get!(plug: plug, url: @url).body == %{"ok" => true}
  end
end
