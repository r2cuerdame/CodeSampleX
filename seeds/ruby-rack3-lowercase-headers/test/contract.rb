require "minitest/autorun"
require "rack"
require "rack/lint"

# Rack 3 requires response header names to be lowercase. The catch is where
# the failure shows up: a bare app returning "Content-Type" works perfectly
# well on its own, and only a server or middleware that lints the response
# rejects it. So the upgrade passes in a unit test and fails in production.
class Rack3HeaderTest < Minitest::Test
  UPPERCASE = ->(_env) { [200, { "Content-Type" => "text/plain" }, ["ok"]] }
  LOWERCASE = ->(_env) { [200, { "content-type" => "text/plain" }, ["ok"]] }

  def env
    Rack::MockRequest.env_for("http://example.com/")
  end

  def test_the_app_itself_does_not_complain
    assert_equal 200, UPPERCASE.call(env).first
  end

  def test_but_lint_rejects_the_uppercase_name
    err = assert_raises(Rack::Lint::LintError) { Rack::Lint.new(UPPERCASE).call(env) }
    assert_match(/uppercase character in header name/, err.message)
  end

  def test_lowercase_passes_lint
    assert_equal 200, Rack::Lint.new(LOWERCASE).call(env).first
  end

  def test_rack_response_downcases_for_you
    # The fix that scales: build the response through Rack::Response and the
    # key is stored lowercase whatever case you wrote.
    res = Rack::Response.new
    res["Content-Type"] = "text/plain"
    assert_equal ["content-type"], res.headers.keys
  end

  def test_headers_lookup_is_case_insensitive
    h = Rack::Headers.new
    h["Content-Type"] = "text/plain"
    assert_equal "text/plain", h["content-type"]
    assert_equal ["content-type"], h.keys
  end

  def test_the_rack2_header_hash_is_gone
    # Rack::Utils::HeaderHash was the Rack 2 way to do this and referencing
    # it is a NameError now, not a deprecation.
    refute defined?(Rack::Utils::HeaderHash)
  end
end
