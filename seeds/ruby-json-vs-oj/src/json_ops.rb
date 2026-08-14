# What Ruby's built-in JSON really does, on a machine where you cannot install
# a faster one.
#
# The standard advice for Ruby JSON is "use oj". On an image with no C
# toolchain that advice is unavailable: oj is a C extension and it cannot be
# built, which the Gemfile records as a measurement rather than a guess. You
# are left with the json Ruby ships, so the useful question becomes which of
# its defaults change your data on the way through.
#
# Five that catch people, in the order they bite:
#
#   1. JSON.parse hands back String keys at every depth. There is no default
#      that changes it. symbolize_names: true is the whole answer, and it
#      touches keys only.
#   2. A custom to_json is honoured by JSON.generate as well as by obj.to_json,
#      so writing one puts you inside the generator — where you get the state
#      as an argument, and where nothing checks that what you returned was JSON
#      at all.
#   3. NaN and Infinity are not JSON. JSON.generate refuses to write them and
#      JSON.parse refuses to read them, both unless allow_nan, so the default
#      round trip of a float that went non-finite raises rather than corrupts.
#      JSON.dump and JSON.load do not share those defaults, which is how a file
#      one wrote becomes a file the other's counterpart cannot read.
#   4. A JSON integer of any size survives exactly, because Ruby has bignums.
#      The same digits with a decimal point become a Float and lose their tail
#      silently. decimal_class is the option that gets them back.
#   5. JSON.pretty_generate is bytes only. The parse of its output is == to the
#      parse of the compact output, every time.
#
# Everything below is what you would actually copy into an app.

require "json"

module JsonOps
  # String keys are the default at every depth; ask for symbols explicitly.
  # Keys only: a String value stays a String.
  def self.parse_symbolized(text)
    JSON.parse(text, symbolize_names: true)
  end

  # Keep the digits a Float would drop. decimal_class takes any class built
  # from the raw token, and String is the one that always works: the documented
  # choice, BigDecimal, is a bundled gem in Ruby 3.4 rather than a default one,
  # so on this image it is both missing from the bundle and unbuildable.
  # Integers are unaffected — decimal_class only sees tokens with a fraction or
  # an exponent.
  def self.parse_exact_decimals(text)
    JSON.parse(text, decimal_class: String)
  end

  # The strict path: what a server should do with a payload it did not write.
  # Non-finite floats raise on the way out and are rejected on the way back in.
  def self.round_trip(value)
    JSON.parse(JSON.generate(value))
  end

  # A value object that serializes correctly from either entry point. The
  # *args is not decoration: JSON.generate passes a JSON::State, and passing it
  # on is what makes indent and space settings reach the nested object.
  class Money
    attr_reader :cents, :currency

    def initialize(cents, currency)
      @cents = cents
      @currency = currency
    end

    def as_json
      { "cents" => cents, "currency" => currency }
    end

    def to_json(*args)
      as_json.to_json(*args)
    end
  end

  # The same class as people write it the first time. Calling it yourself
  # works, which is exactly why the bug ships: it only fails once the object is
  # inside something the generator is walking.
  class NaiveMoney < Money
    def to_json
      as_json.to_json
    end
  end

  # Accepts the state and drops it, which is the version that survives review:
  # valid JSON from both entry points, no exception ever. The cost shows up
  # only inside pretty_generate, where this object is the one line that came
  # out compact, because indent and space live in the state it threw away. No
  # parser can tell the two documents apart, so nothing downstream complains.
  class CompactMoney < Money
    def to_json(*)
      as_json.to_json
    end
  end

  # And the failure that is worse than an exception: the generator splices in
  # whatever to_json returned without looking at it, so an invalid fragment
  # becomes an invalid document that only fails at the reader.
  class LyingMoney < Money
    def to_json(*)
      "$#{format('%.2f', cents / 100.0)}"
    end
  end

  # Which pieces of a C toolchain are on PATH. Empty here, and that is the
  # whole reason oj is not an option: a gem with a native extension has nothing
  # to build with.
  def self.compilers_on_path
    ENV.fetch("PATH", "").split(File::PATH_SEPARATOR).flat_map do |dir|
      Dir.glob(File.join(dir, "{cc,gcc,clang,make}"))
    end
  end
end
