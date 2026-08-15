require "minitest/autorun"
require "rack/test"
require_relative "../src/app"

# Everything below runs the app in this process. Nothing binds a port, and the
# suite passes with the network switched off entirely.
class SinatraStaysInProcessContract < Minitest::Test
  def test_a_modular_app_never_starts_a_server
    # The classic style — require "sinatra" and define routes at the top
    # level — sets run to !test?, and requiring the file installs an at_exit
    # hook that boots a server with that setting. In development that is on,
    # which is how a test run ends up listening on 4567. Subclassing
    # Sinatra::Base is what turns it off, and it is off for the app itself,
    # not merely for this test.
    refute Sinatra::Base.run?
    refute RoutesApp.run?
    assert Sinatra::Application.run?
  end

  def test_the_environment_these_measurements_were_taken_in
    # Sinatra reads the environment once, out of APP_ENV or RACK_ENV, while
    # sinatra/base is being required. Neither is set here, so it is
    # development — and two of the measurements below (the 403 and the 404
    # body) exist only in development. An app that sets :environment later
    # does not move them: they were decided at require time.
    assert_equal :development, Sinatra::Base.environment
    assert_equal :development, RoutesApp.environment
  end
end

class SinatraHostAuthorizationContract < Minitest::Test
  include Rack::Test::Methods

  def app = DefaultHostApp

  def test_a_default_sinatra_4_app_rejects_rack_tests_host_with_403
    # The first wall people hit. rack-test sends Host: example.org and
    # Sinatra 4 mounts Rack::Protection::HostAuthorization, whose development
    # list is localhost, .localhost, .test and any IP literal. The route is
    # never reached, so this looks like a routing bug: 403 with a body that
    # names neither Sinatra nor the setting, and it appears the day you
    # upgrade to Sinatra 4 rather than the day you write the test.
    get "/ping"

    assert_equal 403, last_response.status
    assert_equal "Host not permitted", last_response.body
    assert_equal "text/plain", last_response.content_type
  end

  def test_a_host_on_the_development_list_gets_through_untouched
    # Three fixes, all measured. Send a Host the list already permits...
    header "Host", "localhost"
    get "/ping"
    assert_equal 200, last_response.status
    assert_equal "pong", last_response.body

    # ...anything under .test, since a leading dot means "and subdomains"...
    header "Host", "app.test"
    get "/ping"
    assert_equal 200, last_response.status

    # ...or any IP literal, because the list carries 0.0.0.0/0 and ::/0.
    header "Host", "127.0.0.1"
    get "/ping"
    assert_equal 200, last_response.status
  end

  def test_an_empty_permitted_hosts_switches_the_check_off
    # The fix that does not depend on what the test harness sends: an empty
    # permitted_hosts makes rack-protection accept every request. Running the
    # suite under APP_ENV=test reaches the same place by a different road,
    # because outside development the whole setting is {}.
    assert_equal({ permitted_hosts: [] }, RoutesApp.host_authorization)

    session = Rack::Test::Session.new(RoutesApp)
    session.get "/users/7"
    assert_equal 200, session.last_response.status
  end
end

class SinatraParamsContract < Minitest::Test
  include Rack::Test::Methods

  def app = RoutesApp

  def test_a_named_parameter_is_a_string_under_its_own_key
    get "/users/7"

    assert_equal 200, last_response.status
    assert_equal "7", Capture.snapshot["id"]
    assert_instance_of String, Capture.snapshot["id"]
    assert_equal ["id"], Capture.snapshot.keys
    # No "splat" key exists on a route that has no splat, so code that reaches
    # for params["splat"] first gets nil rather than an empty list.
    refute Capture.snapshot.key?("splat")
  end

  def test_params_is_indifferent_so_the_symbol_works_too
    get "/users/7"

    # Sinatra::IndifferentHash, not Hash: string and symbol reach the same
    # value. It is still a String-keyed hash underneath, which is why keys
    # comes back as strings.
    assert_instance_of Sinatra::IndifferentHash, Capture.snapshot
    assert_equal "7", Capture.snapshot[:id]
    assert_equal ["id"], Capture.snapshot.keys
  end

  def test_a_splat_is_an_array_under_splat_even_when_it_matched_once
    get "/files/report.csv"

    # The asymmetry with a named parameter, and the reason a splat route that
    # was working suddenly interpolates as ["thing"]: one match still arrives
    # as a one-element Array, and it is filed under "splat" rather than under
    # any name you chose. The block argument is the plain String.
    assert_equal ["report.csv"], Capture.snapshot["splat"]
    assert_instance_of Array, Capture.snapshot["splat"]
    assert_equal ["splat"], Capture.snapshot.keys
    assert_equal ["report.csv"], Capture.block_args
  end

  def test_a_splat_swallows_slashes
    # * is not the single-segment wildcard it is in a shell glob or in most
    # router libraries: it crosses / happily, so /files/* also owns every
    # path below it. Sinatra has no separate ** for this.
    get "/files/a/b/c.txt"
    assert_equal ["a/b/c.txt"], Capture.snapshot["splat"]

    # It stops being greedy where the pattern says so. Two splats fill the
    # array in order, and the last one here matches only the extension
    # because the literal dot has to be matched too.
    get "/say/hello/to/world"
    assert_equal %w[hello world], Capture.snapshot["splat"]
    assert_equal %w[hello world], Capture.block_args

    get "/download/path/to/file.xml"
    assert_equal ["path/to/file", "xml"], Capture.snapshot["splat"]
  end

  def test_a_splat_still_needs_something_to_match
    # /files/* is not a match for /files, so the catch-all route people write
    # to serve a section index does not serve the index itself.
    get "/files"
    assert_equal 404, last_response.status
  end

  def test_the_route_capture_wins_over_a_query_parameter_of_the_same_name
    # params merges the query first and the route captures over the top, so
    # a client cannot forge :id by adding ?id= to the URL. Worth knowing in
    # the other direction too: a query parameter is silently unreachable
    # whenever the route names the same key.
    get "/users/7?id=99&extra=x"

    assert_equal "7", Capture.snapshot["id"]
    assert_equal "x", Capture.snapshot["extra"]
  end

  def test_a_query_string_and_a_form_body_merge_and_the_form_wins
    # Both halves are present, so a POST that also carries a query string is
    # not a bug you need to defend against — but on a collision the form body
    # overwrites the query. The merge happens in Rack::Request#params, which
    # is GET.merge(POST); Sinatra copies the result into params and then lays
    # its route captures on top.
    post "/merge?shared=from-query&only_query=q", { "shared" => "from-form", "only_form" => "f" }

    assert_equal 200, last_response.status
    assert_equal "from-form", Capture.snapshot["shared"]
    assert_equal "q", Capture.snapshot["only_query"]
    assert_equal "f", Capture.snapshot["only_form"]

    # The two halves are still available unmerged, which is the only way to
    # tell where a value came from.
    assert_equal({ "shared" => "from-query", "only_query" => "q" }, Capture.rack_get)
    assert_equal({ "shared" => "from-form", "only_form" => "f" }, Capture.rack_post)
  end

  def test_a_route_capture_beats_the_form_body_as_well
    # Full precedence, measured end to end: route capture, then form body,
    # then query string.
    post "/merge/from-route?field=from-query", { "field" => "from-form" }

    assert_equal "from-route", Capture.snapshot["field"]
    assert_equal ["field"], Capture.snapshot.keys
  end

  def test_params_loses_the_route_captures_once_the_route_returns
    # Not a quirk of this test setup: Sinatra merges the captures into the
    # live params hash for the duration of the route and deletes them again
    # in an ensure. Anything that reads params after the block is finished —
    # a lazy enumerator, a stashed reference, a background job handed
    # params — sees the request without its route captures, while the query
    # parameters are still there. Hand work a copy, not params.
    get "/files/a/b/c.txt?q=1"

    assert_equal ["a/b/c.txt"], Capture.snapshot["splat"]
    assert_equal "1", Capture.snapshot["q"]

    refute Capture.live.key?("splat")
    assert_equal "1", Capture.live["q"]
  end

  def test_an_unmatched_path_gets_sinatras_own_404_page_not_an_empty_body
    get "/nope"

    assert_equal 404, last_response.status
    refute_empty last_response.body
    assert_equal "text/html;charset=utf-8", last_response.content_type
    # In development an unmatched path raises Sinatra::NotFound and hits an
    # error handler Sinatra registers on itself, which renders a page telling
    # you to write the route — including the class name of the app that
    # missed. A test asserting an empty 404 body fails here for a reason that
    # has nothing to do with the app.
    assert_includes last_response.body, "know this ditty"
    assert_includes last_response.body, "class RoutesApp"
    assert Sinatra::Base.errors.key?(Sinatra::NotFound)
  end

  def test_halt_404_is_the_one_that_really_is_empty
    # The contrast that makes the page above worth knowing: halting with a
    # bare status never goes near the NotFound handler, so this 404 is the
    # empty one. Same status, completely different body, and which one a test
    # gets depends on whether the route matched at all.
    get "/halted"

    assert_equal 404, last_response.status
    assert_equal "", last_response.body
  end

  def test_the_content_type_is_text_html_even_for_a_json_looking_body
    # Sinatra does not sniff the body. A route that returns a JSON string and
    # forgets content_type serves it as HTML, and every client that switches
    # on the content type — including a rack-test assertion on
    # last_response.content_type — reads it as HTML. The body itself is
    # passed through untouched, so the mistake is invisible until something
    # downstream refuses to parse it.
    get "/looks-like-json"

    assert_equal "text/html;charset=utf-8", last_response.content_type
    assert_equal '{"ok":true}', last_response.body

    # Note the exact string: no space after the semicolon, and the charset is
    # appended for text types. An assertion on "text/html" alone fails.
    refute_equal "text/html", last_response.content_type
  end

  def test_declaring_the_type_replaces_it_and_adds_no_charset
    get "/declared-json"

    assert_equal "application/json", last_response.content_type
    assert_equal '{"ok":true}', last_response.body
  end

  def test_the_header_names_that_come_back_are_lowercase
    # Rack 3 headers are lowercase by contract. Reading through
    # last_response works either way because Rack::Headers is
    # case-insensitive, but anything that inspects the keys — a header
    # allowlist, a diff of two responses — sees "content-type".
    get "/looks-like-json"

    assert_instance_of Rack::Headers, last_response.headers
    assert_includes last_response.headers.keys, "content-type"
    refute_includes last_response.headers.keys, "Content-Type"
    assert_equal last_response.headers["content-type"], last_response.headers["Content-Type"]
  end
end

class SinatraFilterContract < Minitest::Test
  include Rack::Test::Methods

  def app = FilterApp

  def setup
    TRACE.clear
  end

  def test_without_a_stop_every_filter_and_the_route_run_in_order
    get "/guarded"

    assert_equal 200, last_response.status
    assert_equal "route body", last_response.body
    assert_equal %i[filter_1_entered filter_1_completed filter_2_ran route_ran after_filter_ran], TRACE
  end

  def test_return_leaves_the_filter_and_nothing_else
    # The trap. return inside a before block reads like "stop handling this
    # request", and it stops exactly one thing: the rest of that filter.
    # Sinatra compiles each filter block into a method, so return returns from
    # that method — the second filter still runs, the route still runs, and
    # the client gets a 200 from a request the first filter meant to reject.
    # A guard written this way is not a guard.
    get "/guarded?stop=return"

    assert_equal 200, last_response.status
    assert_equal "route body", last_response.body
    refute_includes TRACE, :filter_1_completed
    assert_equal %i[filter_1_entered filter_2_ran route_ran after_filter_ran], TRACE
  end

  def test_halt_stops_the_rest_of_the_chain_and_owns_the_response
    # halt throws :halt, caught outside the whole before-filter-and-route
    # chain, so the second filter and the route never run and the status and
    # body given to halt are what the client gets.
    get "/guarded?stop=halt"

    assert_equal 401, last_response.status
    assert_equal "stopped in filter 1", last_response.body
    refute_includes TRACE, :filter_2_ran
    refute_includes TRACE, :route_ran
  end

  def test_after_filters_still_run_on_a_halted_request
    # halt is not an exit. after filters sit in an ensure, so cleanup and
    # logging still happen — and so does anything in an after filter that
    # assumes the route ran.
    get "/guarded?stop=halt"

    assert_equal %i[filter_1_entered after_filter_ran], TRACE
  end
end
