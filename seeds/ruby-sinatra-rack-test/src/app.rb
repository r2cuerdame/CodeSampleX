# Driving a Sinatra app from a test with rack-test: no server, no port, no
# socket. rack-test calls the Rack app in-process, so everything Sinatra puts
# between Rack and your block — host authorization, filters, the pattern
# matcher, the params merge — still runs for real. That is the point, and it
# is also where the surprises come from.
#
# What this measures, roughly in the order of how much time each one costs
# people:
#
#   1. Sinatra 4 in development answers 403 "Host not permitted" to rack-test's
#      default Host of example.org, before any route runs. Nothing in the 403
#      mentions Sinatra or the setting that produced it.
#   2. params is one hash holding three different things — route captures,
#      the query string, the form body — and the merge order is not the one
#      most people guess.
#   3. A named parameter is a String under its own key; a splat is an Array
#      under "splat", even when it matched exactly once.
#   4. halt and return are not two spellings of the same thing inside a filter.
#   5. The response body of a 404 depends on the environment Sinatra read out
#      of ENV when sinatra/base was required, not on what the app sets later.
#
# require "sinatra/base" + a subclass is the "modular" style, and it is what
# keeps a test run from booting a web server: the classic top-level DSL
# (require "sinatra") registers an at_exit hook that starts one.

require "sinatra/base"

# The routes stash what they saw here. rack-test runs the app inside the test
# process, so the contract reads these back directly instead of inventing a
# serialisation and asserting on a parsed body.
module Capture
  class << self
    attr_accessor :snapshot, :live, :block_args, :rack_get, :rack_post
  end
end

# Filters and the route append to this so the contract can see exactly how far
# a request got.
TRACE = []

class RoutesApp < Sinatra::Base
  # Without this every request below is a 403. Sinatra 4 mounts
  # Rack::Protection::HostAuthorization, and in development the permitted list
  # is localhost, .localhost, .test and any IP literal — rack-test sends
  # Host: example.org, which is on none of them. An empty permitted_hosts
  # turns the check off (rack-protection returns early when the list is
  # empty); APP_ENV=test does the same thing by making the setting {}.
  set :host_authorization, permitted_hosts: []

  # A named parameter. The capture lands under its own key as a String.
  get "/users/:id" do
    # params.dup is deliberate: Sinatra deletes the route captures out of the
    # live hash again once the route returns, so a stashed reference does not
    # show what the route saw. The contract asserts both.
    Capture.snapshot = params.dup
    Capture.live = params
    "named"
  end

  # A splat. The block argument is the matched String; params carries an Array.
  get "/files/*" do |path|
    Capture.snapshot = params.dup
    Capture.live = params
    Capture.block_args = [path]
    "splat"
  end

  get "/say/*/to/*" do |a, b|
    Capture.snapshot = params.dup
    Capture.block_args = [a, b]
    "two splats"
  end

  get "/download/*.*" do
    Capture.snapshot = params.dup
    "extension splat"
  end

  # Query string and form body land in the same params. request.GET and
  # request.POST are captured too, because the merge that decides a collision
  # happens in Rack, not in Sinatra.
  post "/merge" do
    Capture.snapshot = params.dup
    Capture.rack_get = request.GET.dup
    Capture.rack_post = request.POST.dup
    "merged"
  end

  # Same collision with a route capture added on top.
  post "/merge/:field" do
    Capture.snapshot = params.dup
    "merged"
  end

  # A body that any human would call JSON. Sinatra does not look at it.
  get "/looks-like-json" do
    '{"ok":true}'
  end

  get "/declared-json" do
    content_type :json
    '{"ok":true}'
  end

  # halt with a status and nothing else. This is the 404 that really is empty,
  # and it is the contrast that makes Sinatra's own 404 page worth knowing
  # about: an unmatched path does not come out of here.
  get "/halted" do
    halt 404
  end
end

# Two before filters, a route and an after filter, all appending to TRACE, so
# "halt stops the chain and return does not" is a list comparison rather than
# an opinion. The filters are declared with a path so the other apps' requests
# cannot pollute the trace.
class FilterApp < Sinatra::Base
  set :host_authorization, permitted_hosts: []

  before "/guarded" do
    TRACE << :filter_1_entered
    # halt throws :halt. Sinatra catches it outside the whole filter+route
    # chain, so the second filter and the route never run.
    halt 401, "stopped in filter 1" if params["stop"] == "halt"
    # Sinatra compiles each filter block into a method with define_method, so
    # return leaves that one method and nothing else. It reads like an early
    # exit from the request and is only an early exit from this filter.
    return if params["stop"] == "return"

    TRACE << :filter_1_completed
  end

  before "/guarded" do
    TRACE << :filter_2_ran
  end

  get "/guarded" do
    TRACE << :route_ran
    "route body"
  end

  # after filters live in an ensure, so they run even on the halted request.
  after "/guarded" do
    TRACE << :after_filter_ran
  end
end

# Identical to RoutesApp except that it keeps Sinatra's default
# host_authorization, which is what a new app has.
class DefaultHostApp < Sinatra::Base
  get "/ping" do
    "pong"
  end
end
