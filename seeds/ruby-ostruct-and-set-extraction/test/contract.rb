# Recorded before anything can mention Set. Ruby installs an autoload for the
# constant, and the autoload is consumed the first time any code touches Set --
# minitest runs tests in random order, so this has to be captured at load time.
SET_AUTOLOAD_TARGET = Object.autoload?(:Set)

require "minitest/autorun"

require "ostruct"
require "set"

# Ruby's bundled-gem notices go through Warning.warn, not through a write to
# $stderr, so minitest's capture_io does not see them. Collect them here and
# swallow them, which also keeps OpenStruct's redefinition warnings out of the
# test output. Each library below is required in exactly one test on purpose:
# Gem::BUNDLED_GEMS::WARNED suppresses the second notice for the same name.
WARNINGS = []
Warning.singleton_class.prepend(Module.new do
  define_method(:warn) { |message, **_kwargs| WARNINGS << message; nil }
end)

class OstructAndSetExtractionTest < Minitest::Test
  def test_ruby_ships_the_extraction_schedule_and_it_disagrees_with_the_advice
    since = Gem::BUNDLED_GEMS::SINCE

    # The wave that already landed. Nothing puts these back on the load path
    # except a Gemfile line.
    assert_equal "3.4.0", since["abbrev"]
    assert_equal "3.4.0", since["observer"]
    assert_equal "3.4.0", since["drb"]

    # The wave that has not landed. "Add ostruct, Ruby extracted it" is advice
    # about the next release: on 3.4 ostruct is still a default gem and requires
    # fine with no Gemfile entry, so it cannot be the cause of a LoadError you
    # are looking at today.
    assert_equal "4.0.0", since["ostruct"]
    assert_equal "4.0.0", since["benchmark"]
    assert_equal "4.0.0", since["logger"]

    # And set is not on the schedule at all -- it is a default gem with no
    # extraction planned, so it is the one name in this seed that is pinned
    # purely for the version, never to avoid a LoadError.
    refute since.key?("set")
  end

  def test_a_library_from_the_landed_wave_needs_the_gemfile_line
    error = assert_raises(LoadError) { require "observer" }
    assert_equal "observer", error.path

    # abbrev is the same kind of library from the same wave. The only difference
    # is the Gemfile line, and with it the require also lands its monkeypatch on
    # Array, which is what most callers actually use.
    require "abbrev"
    assert Gem.loaded_specs.key?("abbrev")
    assert_equal ["rub", "ruby", "rus", "rust"], %w[ruby rust].abbrev.keys.sort
  end

  def test_the_notice_and_the_loaderror_arrive_together
    WARNINGS.clear
    error = assert_raises(LoadError) { require "drb" }
    notice = WARNINGS.join

    # Ruby prints the "add it to your Gemfile" notice and then fails the require
    # anyway, so the advice and the crash show up in the same run. Reading only
    # the LoadError hides the sentence that names the fix.
    assert_includes notice, "drb was loaded from the standard library"
    assert_includes notice, "is not part of the default gems starting from Ruby 3.4.0"
    assert_includes notice, "You can add drb to your Gemfile"
    assert_equal "drb", error.path
  end

  def test_a_library_from_the_next_wave_only_warns
    WARNINGS.clear
    require "benchmark"
    notice = WARNINGS.join

    # Same notice, different tense, and no exception: this is the upgrade siren
    # for the 4.0 wave. Treat it as the deadline, because after 4.0 this exact
    # require becomes the LoadError above.
    assert defined?(Benchmark)
    assert_includes notice, "will no longer be part of the default gems starting from Ruby 4.0.0"
    assert_includes notice, "You can add benchmark to your Gemfile"

    # It loaded from the interpreter, not from the bundle -- that is what makes
    # it a default gem rather than a resolved dependency.
    refute Gem.loaded_specs.key?("benchmark")
  end

  def test_the_gemfile_line_puts_a_gem_in_front_of_the_interpreters_copy
    ostruct = Gem.loaded_specs.fetch("ostruct")
    assert_equal "0.6.3", ostruct.version.to_s
    refute ostruct.default_gem?

    # Both copies exist at once. The interpreter still carries its own default
    # ostruct; the Gemfile decides which one the process runs, which is why
    # declaring a default gem is a real version pin and not a no-op.
    builtin = Gem::Specification.default_stubs.select { |stub| stub.name == "ostruct" }
    refute_empty builtin
    refute_equal ostruct.version.to_s, builtin.first.version.to_s
  end

  def test_set_resolves_through_the_bundle_although_nothing_requires_it
    # Ruby autoloads Set, so `require "set"` was never needed. That also means a
    # missing Gemfile line for an autoloaded library would fail at the first
    # mention of the constant rather than at a require, which is much harder to
    # place. Here the line exists, so the autoload picks the bundled copy.
    assert_equal "set", SET_AUTOLOAD_TARGET
    assert_equal "1.1.3", Gem.loaded_specs.fetch("set").version.to_s
    assert_includes Set.instance_method(:include?).source_location.first, "set-1.1.3"
  end

  def test_an_openstruct_key_takes_the_method_it_collides_with
    os = OpenStruct.new(name: "widget", class: "premium", method: "card")

    # The trap, and it is the opposite of the usual claim. OpenStruct does not
    # protect real methods from a colliding key: it defines a singleton reader
    # and writer per key, so the data wins outright and #class stops answering
    # with a Class.
    assert_equal "premium", os.class
    assert_equal %i[class class= method method= name name=], os.singleton_methods.sort

    # Anything downstream that treats #class as a Class breaks on a String, and
    # #method is now a zero-argument reader, so reflection is gone too. Both
    # failures land far away from the hash that caused them.
    assert_raises(NoMethodError) { os.class.name }
    assert_raises(ArgumentError) { os.method(:name) }

    # The object is still an OpenStruct. Only the accessor was taken, so type
    # checks that do not go through #class keep working, and there is a way back.
    assert os.is_a?(OpenStruct)
    assert_equal OpenStruct, Object.instance_method(:class).bind_call(os)

    # Read through [] or to_h and no key can shadow anything. This is the fix
    # for any OpenStruct built out of data you did not author.
    assert_equal "premium", os[:class]
    assert_equal "card", os.to_h.fetch(:method)
  end

  def test_openstruct_overwrites_even_send
    WARNINGS.clear
    os = OpenStruct.new(:__send__ => "data")

    # Ruby objects out loud to this one and OpenStruct does it anyway, which is
    # how far "the data wins" goes. The escape hatch people reach for when a
    # method is shadowed is itself shadowed.
    assert_includes WARNINGS.join, "redefining '__send__' may cause serious problems"
    assert_equal "data", os.__send__
    assert_raises(ArgumentError) { os.__send__(:to_h) }
  end

  def test_an_openstruct_typo_is_silent
    os = OpenStruct.new(name: "widget")

    # No key, no error, in either accessor. A renamed field reads as nil all the
    # way to production.
    assert_nil os.nmae
    assert_nil os[:nmae]
    refute os.respond_to?(:nmae)

    # to_h.fetch is the read that fails where the mistake is.
    assert_raises(KeyError) { os.to_h.fetch(:nmae) }
  end

  def test_set_dedupes_by_eql_and_cannot_be_rehashed
    # Set indexes by hash and eql?, exactly like Hash keys, so numeric values
    # that are == to each other are still two members.
    assert_equal 1, 1.0
    refute 1.eql?(1.0)
    assert_equal 2, Set[1, 1.0].size

    # Mutating a member after insertion moves it to a bucket the set does not
    # look in, so it stops finding a member it still holds.
    row = ["x"]
    set = Set[row]
    assert_includes set, row
    row << "y"
    refute_includes set, row
    assert_includes set.to_a, row

    # Hash exposes rehash to repair exactly this. Set does not, so the repair is
    # a rebuild -- which is the argument for keeping set members frozen.
    refute Set.method_defined?(:rehash)
    assert_respond_to({}, :rehash)
    assert_includes Set.new(set.to_a), row
  end
end
