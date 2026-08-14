require "minitest/autorun"
require_relative "../src/json_ops"

# Keeps whatever the generator handed to to_json, so the argument can be
# identified rather than inferred from the arity error.
class StateProbe
  attr_reader :received

  def initialize
    @received = []
  end

  def to_json(*args)
    @received.concat(args)
    "0"
  end
end

class JsonDefaultsContract < Minitest::Test
  MONEY = JsonOps::Money.new(1234, "USD").freeze
  MONEY_JSON = '{"cents":1234,"currency":"USD"}'.freeze

  def test_parse_returns_string_keys_and_symbolize_names_is_the_option_people_forget
    doc = '{"total":2,"items":[{"sku":"a-1","qty":1}]}'

    assert_equal %w[total items], JSON.parse(doc).keys
    assert_equal %w[sku qty], JSON.parse(doc)["items"][0].keys

    symbolized = JsonOps.parse_symbolized(doc)
    assert_equal %i[total items], symbolized.keys
    assert_equal %i[sku qty], symbolized[:items][0].keys

    # Keys only. The value stays the String it was, which is what makes
    # symbolize_names safe to use on a payload you did not write.
    assert_equal "a-1", symbolized[:items][0][:sku]
    assert_instance_of String, symbolized[:items][0][:sku]
  end

  def test_symbol_keys_do_not_survive_a_round_trip_on_their_own
    # The direction people forget: generate is happy to take symbol keys, so
    # the write side looks fine and the read side quietly hands back strings.
    assert_equal '{"cents":1234}', JSON.generate({ cents: 1234 })
    assert_equal({ "cents" => 1234 }, JsonOps.round_trip({ cents: 1234 }))
    refute_equal({ cents: 1234 }, JsonOps.round_trip({ cents: 1234 }))
  end

  def test_a_key_that_was_not_a_string_becomes_one_and_symbolize_names_cannot_undo_it
    # JSON object keys are strings by definition, so generate calls to_s on
    # whatever you used. A Hash keyed by Integer is not a round trip, and
    # asking for symbols afterwards gets you the symbol :"1", not 1.
    assert_equal '{"1":"a","b":"c"}', JSON.generate({ 1 => "a", :b => "c" })
    assert_equal({ "1" => "a" }, JsonOps.round_trip({ 1 => "a" }))
    assert_equal({ :"1" => "a" }, JsonOps.parse_symbolized(JSON.generate({ 1 => "a" })))

    # And the collision that follows is silent, in two steps: the generator
    # writes both keys, because a Hash that has two distinct keys has two
    # entries, and the parser then keeps the last one.
    assert_equal '{"1":"a","1":"b"}', JSON.generate({ 1 => "a", "1" => "b" })
    assert_equal({ "1" => "b" }, JsonOps.round_trip({ 1 => "a", "1" => "b" }))
    assert_equal({ "a" => 2 }, JSON.parse('{"a":1,"a":2}'))
  end

  def test_generate_and_to_json_agree_when_to_json_accepts_the_state
    assert_equal MONEY_JSON, MONEY.to_json
    assert_equal MONEY_JSON, JSON.generate(MONEY)
    assert_equal %({"price":#{MONEY_JSON}}), JSON.generate({ "price" => MONEY })

    # What to_json receives is a JSON::State, and only a direct call passes
    # nothing at all — which is why an arity-zero to_json passes its own unit
    # test and fails everywhere else.
    probe = StateProbe.new
    probe.to_json
    assert_empty probe.received
    JSON.generate({ "n" => probe })
    assert_kind_of JSON::State, probe.received.fetch(0)

    # Passing *args through is what carries that state down. The object that
    # accepts the state and drops it is the trap here: it is the one line of a
    # pretty document that came out compact, and since both documents parse to
    # the same data nothing downstream will ever report it.
    passed_on = JSON.pretty_generate({ "price" => MONEY })
    dropped = JSON.pretty_generate({ "price" => JsonOps::CompactMoney.new(1234, "USD") })

    assert_includes passed_on, %(\n    "cents": 1234,\n)
    assert_equal %({\n  "price": #{MONEY_JSON}\n}), dropped
    assert_equal JSON.parse(passed_on), JSON.parse(dropped)
  end

  def test_an_arity_zero_to_json_only_works_when_you_call_it_yourself
    # Measured correction to the usual framing. The split is not
    # JSON.generate versus to_json — generate honours a custom to_json too.
    # It is arity: everything except a direct call passes a JSON::State, so a
    # `def to_json` with no parameters passes its own unit test and then
    # raises ArgumentError the moment the object is nested in anything.
    naive = JsonOps::NaiveMoney.new(1234, "USD")

    assert_equal MONEY_JSON, naive.to_json

    err = assert_raises(ArgumentError) { JSON.generate(naive) }
    assert_equal "wrong number of arguments (given 1, expected 0)", err.message
    assert_raises(ArgumentError) { JSON.generate({ "price" => naive }) }
    assert_raises(ArgumentError) { { "price" => naive }.to_json }
  end

  def test_the_generator_never_checks_what_to_json_returned
    # Worse than an exception: to_json is spliced in verbatim, so a fragment
    # that is not JSON produces a document that is not JSON. Nothing fails on
    # the writing side; the failure surfaces at whoever reads it.
    lying = JsonOps::LyingMoney.new(1234, "USD")
    written = JSON.generate({ "price" => lying })

    assert_equal '{"price":$12.34}', written
    err = assert_raises(JSON::ParserError) { JSON.parse(written) }
    assert_includes err.message, "$12.34"
  end

  def test_an_object_with_no_to_json_becomes_its_to_s_unless_you_ask_for_strict
    # The default for an unknown object is not an error, it is the object's
    # to_s wrapped in quotes — an address string shipped to a client that
    # expected an object. strict: true is what turns it back into a failure.
    assert_match(/\A"#<Object/, JSON.generate(Object.new))

    err = assert_raises(JSON::GeneratorError) { JSON.generate(Object.new, strict: true) }
    assert_equal "Object not allowed in JSON", err.message

    # Symbols go the same way, which is why a Hash of symbol values looks
    # correct right up to the point someone turns strict on.
    assert_equal '"paid"', JSON.generate(:paid)
    assert_raises(JSON::GeneratorError) { JSON.generate(:paid, strict: true) }
  end

  def test_a_round_trip_of_a_non_finite_float_raises_at_the_first_step
    # NaN and Infinity are not in the JSON grammar. Both directions refuse
    # them by default, so a division that went non-finite fails loudly at
    # serialization rather than arriving somewhere as null.
    err = assert_raises(JSON::GeneratorError) { JSON.generate({ "ratio" => Float::NAN }) }
    assert_equal "NaN not allowed in JSON", err.message
    assert_raises(JSON::GeneratorError) { JSON.generate({ "ratio" => Float::INFINITY }) }
    assert_raises(JSON::GeneratorError) { JsonOps.round_trip(Float::NAN) }

    # to_json is not a way around it: same generator, same refusal.
    assert_raises(JSON::GeneratorError) { Float::NAN.to_json }

    # allow_nan is per call and needed on both sides. Writing with it and
    # reading without is the trap, because the output looks like JSON.
    written = JSON.generate({ "ratio" => Float::INFINITY }, allow_nan: true)
    assert_equal '{"ratio":Infinity}', written
    assert_raises(JSON::ParserError) { JSON.parse(written) }
    assert_equal 1, JSON.parse(written, allow_nan: true)["ratio"].infinite?
    assert JSON.parse('{"ratio":NaN}', allow_nan: true)["ratio"].nan?

    # One rescue covers both directions.
    assert_operator JSON::GeneratorError, :<, JSON::JSONError
    assert_operator JSON::ParserError, :<, JSON::JSONError
  end

  def test_dump_and_load_do_not_carry_generate_and_parse_defaults
    # The pair that quietly disagrees with the pair above: JSON.dump allows
    # non-finite floats, so it writes a document JSON.parse cannot read back.
    # Only its own counterpart, JSON.load, will take it.
    assert_equal true, JSON.dump_default_options[:allow_nan]

    written = JSON.dump({ "ratio" => Float::NAN })
    assert_equal '{"ratio":NaN}', written
    assert_raises(JSON::ParserError) { JSON.parse(written) }
    assert JSON.load(written)["ratio"].nan?

    # allow_blank is the same asymmetry on the empty input every HTTP client
    # eventually hands you.
    assert_equal true, JSON.load_default_options[:allow_blank]
    assert_nil JSON.load("")
    assert_raises(JSON::ParserError) { JSON.parse("") }
  end

  def test_an_integer_survives_exactly_where_the_same_digits_as_a_float_do_not
    # Ruby has bignums, so the JavaScript ceiling does not apply: 2**53 + 1
    # and a thirty digit integer come back byte for byte.
    assert_equal 9_007_199_254_740_993, JSON.parse("[9007199254740993]")[0]
    assert_instance_of Integer, JSON.parse("[9007199254740993]")[0]
    assert_equal "[9007199254740993]", JSON.generate(JSON.parse("[9007199254740993]"))
    assert_equal "[123456789012345678901234567890]",
                 JSON.generate(JSON.parse("[123456789012345678901234567890]"))

    # A decimal point changes the type, and Float is where the digits go. The
    # same value written with .0 loses the low bit, and even the text it
    # regenerates as no longer looks like what was sent.
    assert_equal 9_007_199_254_740_992.0, JSON.parse("[9007199254740993.0]")[0]
    assert_equal "[9.007199254740992e+15]", JSON.generate(JSON.parse("[9007199254740993.0]"))
    assert_equal 1.0, JSON.parse("[1.0000000000000000001]")[0]

    # decimal_class: BigDecimal is the documented answer and it is not one
    # here. bigdecimal is not a default gem on Ruby 3.4, so bundler hides the
    # copy that shipped with the image the way it hides anything the Gemfile
    # did not ask for — the spec is not even visible — and installing it hits
    # the same missing compiler as oj, being a C extension too.
    assert_empty Gem::Specification.find_all_by_name("bigdecimal")

    # String is the decimal_class that needs nothing, and it only touches
    # tokens that had a fraction or an exponent, so ids stay Integer while
    # money keeps every digit.
    exact = JsonOps.parse_exact_decimals('{"id":9007199254740993,"rate":1.0000000000000000001}')
    assert_equal 9_007_199_254_740_993, exact["id"]
    assert_equal "1.0000000000000000001", exact["rate"]
  end

  def test_pretty_generate_changes_the_bytes_and_nothing_else
    doc = { "b" => [1, 2], "a" => { "x" => nil }, "empty_list" => [], "empty_map" => {} }
    compact = JSON.generate(doc)
    pretty = JSON.pretty_generate(doc)

    refute_equal compact, pretty
    assert_operator pretty.bytesize, :>, compact.bytesize
    assert_equal JSON.parse(compact), JSON.parse(pretty)

    # Insertion order is preserved by both, so a diff of two pretty documents
    # is a diff of the data. Empty containers stay on one line, and there is
    # no trailing newline — append one yourself before writing a file.
    assert_equal %w[b a empty_list empty_map], JSON.parse(compact).keys
    assert_equal %w[b a empty_list empty_map], JSON.parse(pretty).keys
    assert_includes pretty, %("empty_list": [],)
    assert_includes pretty, %("empty_map": {})
    refute pretty.end_with?("\n")
  end

  def test_the_json_in_use_is_the_c_extension_ruby_ships_and_nothing_could_replace_it_here
    # The tripwire for the Gemfile's claim. Nothing in the bundle provides
    # json — Gem.loaded_specs lists only what the Gemfile resolved, and json
    # is not in it — yet JSON answers anyway, because it is a default gem
    # baked into the Ruby installation.
    refute Gem.loaded_specs.key?("json")
    spec = Gem::Specification.find_all_by_name("json").fetch(0)

    assert spec.default_gem?
    assert_equal "2.9.1", JSON::VERSION
    assert_equal JSON::VERSION, spec.version.to_s

    # And it is the compiled parser and generator, not a pure-Ruby fallback:
    # both are .so files under the interpreter's own archdir, built when Ruby
    # was built. That is the fast path oj would be replacing — and the
    # compiler that produced it is not in the image, which is why oj cannot
    # be installed and why requiring it is a LoadError rather than a
    # benchmark.
    archdir = RbConfig::CONFIG["archdir"]
    assert_includes $LOADED_FEATURES, File.join(archdir, "json/ext/parser.so")
    assert_includes $LOADED_FEATURES, File.join(archdir, "json/ext/generator.so")
    assert_equal JSON::Ext::Generator, JSON.generator
    assert_equal JSON::Ext::Parser, JSON.parser

    assert_empty JsonOps.compilers_on_path
    assert_raises(LoadError) { require "oj" }
  end
end
