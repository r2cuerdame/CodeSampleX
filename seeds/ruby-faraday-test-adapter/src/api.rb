# Testing Faraday code without a network, and what Faraday 2 actually took
# out of core.
#
# The advice you find for Faraday 2 is "adapters and middleware moved out of
# core, so install the extra gems". Half of that is wrong, and guessing which
# half costs you a Gemfile full of gems that do not exist. Faraday::Adapter::Test
# and the json / raise_error response middleware all ship inside faraday
# itself; what moved out is the *default* adapter, net_http, which faraday
# still pulls in as a dependency. The contract measures where each of those
# classes is loaded from instead of taking anyone's word for it.
#
# The test adapter is the Ruby equivalent of an httpx MockTransport: it
# replaces only the bottom of the stack, so url_prefix, query encoding and
# every middleware you registered still run for real.
#
# Two things Faraday does not do for you, both of which look like the
# adapter misbehaving: it does not parse a JSON body (that is
# `f.response :json`, and even then only when the response carries a JSON
# content type) and it does not raise on a 5xx (that is
# `f.response :raise_error`).

require "faraday"

module Api
  # A connection that never opens a socket. Middleware order is deliberate:
  # response middleware runs bottom-up, so raise_error registered first —
  # outermost — sees the body json already parsed.
  def self.build(stubs, json: false, raise_error: false)
    Faraday.new(url: "https://api.example.com") do |f|
      f.response :raise_error if raise_error
      f.response :json if json
      f.adapter :test, stubs
    end
  end

  def self.fetch_items(conn, page)
    conn.get("/items", { "page" => page.to_s })
  end

  # Which installed gem physically ships a class, resolved from the file the
  # method was defined in. This is the only honest way to answer "do I need
  # another gem for this".
  def self.shipping_gem(klass, method_name)
    file, = klass.instance_method(method_name).source_location
    spec = Gem.loaded_specs.values.find { |s| file.start_with?(s.full_gem_path) }
    spec ? "#{spec.name}-#{spec.version}" : file
  end
end
