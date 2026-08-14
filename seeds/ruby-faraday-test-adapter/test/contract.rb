require "minitest/autorun"
require_relative "../src/api"

class FaradayTestAdapterContract < Minitest::Test
  Stubs = Faraday::Adapter::Test::Stubs

  JSON_BODY = '{"items":[1,2],"page":"2"}'.freeze
  JSON_TYPE = { "content-type" => "application/json" }.freeze

  def test_stub_matches_on_path_and_query
    stubs = Stubs.new do |stub|
      stub.get("/items?page=2") { [200, JSON_TYPE, JSON_BODY] }
    end
    res = Api.fetch_items(Api.build(stubs, json: true), 2)

    assert_equal 200, res.status
    assert res.success?
    assert_equal({ "items" => [1, 2], "page" => "2" }, res.body)
    stubs.verify_stubbed_calls
  end

  def test_a_query_the_stub_declared_must_match_exactly
    # The stub's query is a filter, not a description: a request whose page
    # is 3 does not reach a stub written for page=2, and the failure is a
    # NotFound that names the URL rather than a wrong-looking response.
    stubs = Stubs.new do |stub|
      stub.get("/items?page=2") { [200, JSON_TYPE, JSON_BODY] }
    end
    err = assert_raises(Stubs::NotFound) { Api.fetch_items(Api.build(stubs), 3) }
    assert_match(/page=3/, err.message)
  end

  def test_stub_params_are_a_subset_filter_not_an_exact_match
    # The asymmetry that catches people: only the keys the stub names are
    # compared, so a stub is a filter over the request and never a
    # description of it. A stub written without a query is a wildcard over
    # every query string, and a stub written for page=2 still answers a
    # request that also sent sort=asc. Leaving the query off does not
    # tighten the test, it loosens it.
    wildcard = Stubs.new { |stub| stub.get("/items") { [200, JSON_TYPE, JSON_BODY] } }
    assert_equal 200, Api.fetch_items(Api.build(wildcard), 99).status

    partial = Stubs.new { |stub| stub.get("/items?page=2") { [200, JSON_TYPE, JSON_BODY] } }
    conn = Api.build(partial)
    assert_equal 200, conn.get("/items", { "page" => "2", "sort" => "asc" }).status
  end

  def test_an_unstubbed_path_raises_not_found
    stubs = Stubs.new { |stub| stub.get("/items") { [200, JSON_TYPE, JSON_BODY] } }
    err = assert_raises(Stubs::NotFound) { Api.build(stubs).get("/other") }
    assert_match(%r{/other}, err.message)
  end

  def test_the_connection_url_prefix_reaches_the_matcher
    # The adapter replaces only the bottom of the stack, so by the time
    # matching runs the host from url_prefix is already part of the request:
    # a stub written as a full URL matches, and the same stub aimed at
    # another host does not. Worth writing that way once a suite talks to
    # two services, since a bare path stub answers for either of them.
    here = Stubs.new do |stub|
      stub.get("https://api.example.com/items") { [200, JSON_TYPE, JSON_BODY] }
    end
    assert_equal 200, Api.build(here).get("/items").status

    elsewhere = Stubs.new do |stub|
      stub.get("https://other.example.com/items") { [200, JSON_TYPE, JSON_BODY] }
    end
    err = assert_raises(Stubs::NotFound) { Api.build(elsewhere).get("/items") }
    assert_match(%r{https://api\.example\.com/items}, err.message)
  end

  def test_a_500_without_raise_error_is_only_a_response
    stubs = Stubs.new { |stub| stub.get("/boom") { [500, JSON_TYPE, '{"error":"nope"}'] } }
    res = Api.build(stubs, json: true).get("/boom")

    refute res.success?
    assert_equal 500, res.status
    assert_equal({ "error" => "nope" }, res.body)
  end

  def test_raise_error_turns_the_500_into_a_server_error
    stubs = Stubs.new { |stub| stub.get("/boom") { [500, JSON_TYPE, '{"error":"nope"}'] } }
    err = assert_raises(Faraday::ServerError) do
      Api.build(stubs, json: true, raise_error: true).get("/boom")
    end

    assert_kind_of Faraday::Error, err
    assert_equal 500, err.response[:status]
    # raise_error is registered outermost, so json has already run by the
    # time it fires and the body on the exception is parsed.
    assert_equal({ "error" => "nope" }, err.response[:body])
  end

  def test_middleware_order_decides_what_the_exception_carries
    # Response middleware completes innermost-first. Put raise_error closest
    # to the adapter and it wins the race against json, so the exception
    # carries the raw string — the same code, reordered, reports differently.
    stubs = Stubs.new { |stub| stub.get("/boom") { [500, JSON_TYPE, '{"error":"nope"}'] } }
    conn = Faraday.new(url: "https://api.example.com") do |f|
      f.response :json
      f.response :raise_error
      f.adapter :test, stubs
    end
    err = assert_raises(Faraday::ServerError) { conn.get("/boom") }
    assert_equal '{"error":"nope"}', err.response[:body]
  end

  def test_a_404_maps_to_its_own_subclass
    stubs = Stubs.new { |stub| stub.get("/gone") { [404, JSON_TYPE, "{}"] } }
    conn = Api.build(stubs, raise_error: true)
    err = assert_raises(Faraday::ResourceNotFound) { conn.get("/gone") }
    assert_equal 404, err.response[:status]
  end

  def test_json_is_not_parsed_unless_you_add_the_middleware
    stubs = Stubs.new { |stub| stub.get("/items") { [200, JSON_TYPE, JSON_BODY] } }
    body = Api.build(stubs).get("/items").body

    assert_instance_of String, body
    assert_equal JSON_BODY, body
  end

  def test_the_json_middleware_needs_a_json_content_type
    # Second half of the same trap: the middleware is registered but the
    # stub returned no content type, so nothing parses and you are back to a
    # String. Stubs have to set the header the real server would.
    stubs = Stubs.new { |stub| stub.get("/items") { [200, {}, JSON_BODY] } }
    assert_instance_of String, Api.build(stubs, json: true).get("/items").body
  end

  def test_verify_stubbed_calls_fails_on_a_stub_that_went_unused
    stubs = Stubs.new do |stub|
      stub.get("/items") { [200, JSON_TYPE, JSON_BODY] }
      stub.get("/never-called") { [200, JSON_TYPE, JSON_BODY] }
    end
    conn = Api.build(stubs)
    conn.get("/items")

    err = assert_raises(RuntimeError) { stubs.verify_stubbed_calls }
    assert_match(%r{/never-called}, err.message)
  end

  def test_a_consumed_stub_still_answers_a_second_identical_call
    # Matched stubs are moved to a consumed list, not deleted, so a retry
    # loop under test does not fall off a cliff on the second attempt.
    stubs = Stubs.new { |stub| stub.get("/items") { [200, JSON_TYPE, JSON_BODY] } }
    conn = Api.build(stubs)

    assert_equal 200, conn.get("/items").status
    assert_equal 200, conn.get("/items").status
    stubs.verify_stubbed_calls
  end

  def test_the_test_adapter_and_both_middlewares_ship_inside_faraday
    # The migration answer, measured: nothing here needs an extra gem.
    core = "faraday-#{Faraday::VERSION}"

    assert_equal Faraday::Adapter::Test, Faraday::Adapter.lookup_middleware(:test)
    assert_equal core, Api.shipping_gem(Faraday::Adapter::Test, :call)
    assert_equal core, Api.shipping_gem(Faraday::Response::Json, :on_complete)
    assert_equal core, Api.shipping_gem(Faraday::Response::RaiseError, :on_complete)
  end

  def test_the_default_adapter_is_the_piece_that_moved_out
    # net_http is a separate gem faraday depends on — which is why the
    # default connection needs no setup and why it is not a stub.
    assert_equal :net_http, Faraday.default_adapter
    net_http = Faraday::Adapter.lookup_middleware(:net_http)
    assert_match(/\Afaraday-net_http-/, Api.shipping_gem(net_http, :call))
  end

  def test_an_unbundled_adapter_is_simply_not_registered
    # The adapters that really did leave core fail at lookup, not at request
    # time, so a typo and a missing gem look identical.
    err = assert_raises(Faraday::Error) { Faraday::Adapter.lookup_middleware(:typhoeus) }
    assert_equal ":typhoeus is not registered on Faraday::Adapter", err.message
  end

  def test_the_json_middleware_runs_with_no_json_gem_in_the_bundle
    # Nothing in the Gemfile provides json — the middleware requires the one
    # Ruby ships. That stops being true at faraday 2.12, which adds a runtime
    # dependency on the json gem, and json compiles a C extension: on an
    # alpine image with no toolchain the install fails there, long before any
    # of this runs. This assertion is the tripwire for that upgrade.
    refute Gem.loaded_specs.key?("json")
    assert defined?(JSON::VERSION)
  end

  def test_forgetting_the_test_adapter_opens_a_real_socket
    # Proof that the stubs are what keeps the suite offline: the same code
    # with the default adapter goes to the network. Loopback port 9 is
    # closed, so this refuses immediately instead of hanging.
    conn = Faraday.new(url: "http://127.0.0.1:9")
    assert_raises(Faraday::ConnectionFailed) { conn.get("/items") }
  end
end
