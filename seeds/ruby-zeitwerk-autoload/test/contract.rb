require "minitest/autorun"
require_relative "../src/autoloading"

class ZeitwerkAutoloadContract < Minitest::Test
  def test_a_conventional_name_autoloads_on_first_reference
    file = Autoloading.file("conventional", "json_parser.rb")
    Autoloading.loader_for("conventional")

    # setup installs a Ruby autoload; it does not read or run the file. Note
    # const_defined? already says true here — Ruby counts a pending autoload as
    # a defined constant, so const_defined? cannot tell you whether anything
    # loaded. autoload? can: it returns the file until the load happens.
    assert_equal file, Autoloading.pending_autoload(:JsonParser)
    assert Object.const_defined?(:JsonParser, false)
    refute Autoloading.loaded?(:JsonParser)

    assert_equal "file body evaluated", JsonParser::LOADED_AT
    assert_equal "x", JsonParser.parse("x")

    assert_nil Autoloading.pending_autoload(:JsonParser)
    assert Autoloading.loaded?(:JsonParser)
  end

  def test_the_naming_rule_is_underscore_camelization_not_ruby_acronym_style
    # The whole rule, with no loader involved: split the basename on
    # underscores, capitalise each part, join. Acronyms are not special. This is
    # why http_client.rb has to define HttpClient even though every Ruby style
    # guide, and the standard library itself, would write HTTPClient.
    inflector = Zeitwerk::Inflector.new
    path = Autoloading.file("mismatch", "http_client.rb")

    assert_equal "HttpClient", inflector.camelize("http_client", path)
    assert_equal "JsonParser", inflector.camelize("json_parser", path)
    assert_equal "ApiGateway", inflector.camelize("api_gateway", path)
    assert_equal "CsvWriter",  inflector.camelize("csv_writer", path)
  end

  def test_a_mismatch_raises_zeitwerk_name_error_naming_the_file_and_the_expected_constant
    file = Autoloading.file("mismatch", "http_client.rb")
    Autoloading.loader_for("mismatch")

    assert_equal file, Autoloading.pending_autoload(:HttpClient)

    err = assert_raises(Zeitwerk::NameError) { HttpClient }

    # The exact message, in full. Two halves: the file Zeitwerk loaded, and the
    # constant Zeitwerk expected that file to define.
    assert_equal "expected file #{file} to define constant HttpClient, but didn't", err.message
    assert_equal :HttpClient, err.name

    # Measured, and the opposite of what the wording "naming both" suggests:
    # the message never mentions HTTPClient, the constant the file really
    # defines. Zeitwerk does not parse the file, it installs an autoload for one
    # name and then checks whether that name appeared. Anything else the file
    # defined is invisible to it, so the error cannot point at the typo — you
    # have to open the file.
    refute_match(/HTTPClient/, err.message)

    # It is a ::NameError subclass, so `rescue NameError` swallows it and the
    # bug looks like an ordinary missing constant.
    assert_kind_of ::NameError, err
  end

  def test_a_failed_autoload_leaves_the_wrong_constant_loaded_and_degrades_the_second_error
    file = Autoloading.file("aftermath", "soap_client.rb")
    Autoloading.loader_for("aftermath")

    first = assert_raises(Zeitwerk::NameError) { Object.const_get(:SoapClient) }
    assert_equal "expected file #{file} to define constant SoapClient, but didn't", first.message

    # The file was required and fully evaluated before Zeitwerk checked. The
    # wrongly named constant is live, and require has already recorded the file,
    # so nothing will load it a second time.
    assert Object.const_defined?(:SOAPClient, false)
    assert Object.const_get(:SOAPClient)::RAN
    assert_includes $LOADED_FEATURES, file

    # Zeitwerk removes the autoload itself after the failure. It has to: it
    # raises from inside the require its own autoload triggered, and Ruby keeps
    # an autoload registered when the require ends in an exception. Both halves
    # of that are measured against plain Object.autoload in the next test. The
    # cost of the removal is that the good error happens exactly once.
    assert_nil Object.autoload?(:SoapClient)
    refute Object.const_defined?(:SoapClient, false)

    second = assert_raises(::NameError) { Object.const_get(:SoapClient) }
    refute_instance_of Zeitwerk::NameError, second
    assert_equal :SoapClient, second.name
    assert_match(/uninitialized constant SoapClient/, second.message)
    refute_match(/expected file/, second.message)
  end

  def test_plain_ruby_autoload_only_differs_when_the_require_raises
    # No Zeitwerk in this test at all. "Zeitwerk is stricter than Ruby about a
    # file that does not define the constant" is the usual summary, and measured
    # against Object.autoload it is only true in one of the two shapes.
    misnamed = Autoloading.file("plain_ruby", "legacy_uri.rb")
    Object.autoload(:LegacyUri, misnamed)
    assert_equal misnamed, Object.autoload?(:LegacyUri)

    err = assert_raises(::NameError) { Object.const_get(:LegacyUri) }
    assert_match(/uninitialized constant LegacyUri/, err.message)

    # Identical end state to the Zeitwerk failure above: the wrongly named
    # constant is live, the file is in $LOADED_FEATURES, the autoload is gone
    # and the constant is undefined. When the file merely defines the wrong
    # name, Ruby drops the autoload on its own — nothing to be stricter than.
    assert Object.const_defined?(:LegacyURI, false)
    assert_includes $LOADED_FEATURES, misnamed
    assert_nil Object.autoload?(:LegacyUri)
    refute Object.const_defined?(:LegacyUri, false)

    # The shape where Ruby really does hold on to the autoload is a require that
    # raises, and that is exactly the shape Zeitwerk is in when it reports a
    # mismatch. Here the constant stays pending, the file never reaches
    # $LOADED_FEATURES, and the next reference runs the file again.
    raising = Autoloading.file("plain_ruby", "raises_on_load.rb")
    Object.autoload(:RaisesOnLoad, raising)
    assert_raises(RuntimeError) { Object.const_get(:RaisesOnLoad) }

    assert_equal raising, Object.autoload?(:RaisesOnLoad)
    refute_includes $LOADED_FEATURES, raising
    assert_equal 1, Thread.current[:csx_raises_on_load_runs]

    assert_raises(RuntimeError) { Object.const_get(:RaisesOnLoad) }
    assert_equal 2, Thread.current[:csx_raises_on_load_runs]

    # So Zeitwerk's extra removal is what turns its own raise into the plain
    # uninitialized-constant error asserted above, instead of a constant that
    # stays pending and re-runs its file on every reference.
  end

  def test_an_inflector_override_makes_the_acronym_spelling_correct
    file = Autoloading.file("inflected", "api_gateway.rb")
    loader = Autoloading.loader_for("inflected", inflections: { "api_gateway" => "APIGateway" })

    # inflect takes the basename without the .rb extension, and it replaces the
    # derived name outright: ApiGateway is not registered at all, so it is the
    # override and not an alias.
    assert_equal file, Autoloading.pending_autoload(:APIGateway)
    assert_nil Object.autoload?(:ApiGateway)
    refute Object.const_defined?(:ApiGateway, false)

    assert_equal "/health", APIGateway.route("/health")
    assert Autoloading.loaded?(:APIGateway)

    # And the tree now passes the strict check, which is the practical test that
    # an override is complete: eager_load returns instead of raising.
    loader.eager_load
    assert Autoloading.loaded?(:APIGateway)
  end

  def test_a_file_that_defines_nothing_fails_the_same_way_after_running
    file = Autoloading.file("silent", "audit_log.rb")
    Autoloading.loader_for("silent")

    err = assert_raises(Zeitwerk::NameError) { AuditLog }
    assert_equal "expected file #{file} to define constant AuditLog, but didn't", err.message
    assert_equal :AuditLog, err.name

    # Same failure as a misspelling, and the same order of events: the body ran
    # first. A file holding only requires or monkey patches cannot sit inside a
    # managed directory, it has to be ignored or moved out.
    assert_equal true, Thread.current[:csx_audit_log_file_ran]
  end

  def test_eager_load_moves_the_failure_to_boot_but_stops_at_the_first_mismatch
    loader = Autoloading.loader_for("eager")
    csv = Autoloading.file("eager", "csv_writer.rb")
    xml = Autoloading.file("eager", "xml_writer.rb")

    # Zeitwerk sorts directory entries itself so eager loading is deterministic
    # across file systems, which is what makes this assertion stable: of
    # csv_writer, plain_report and xml_writer, csv_writer goes first.
    first = assert_raises(Zeitwerk::NameError) { loader.eager_load }
    assert_equal "expected file #{csv} to define constant CsvWriter, but didn't", first.message

    # Measured, and it refutes the usual pitch for eager loading: this is NOT an
    # audit that reports every mismatch at once. eager_load raises, so it aborts.
    # The second broken file was never opened and the correctly named file
    # between them was never loaded either.
    assert_equal Autoloading.file("eager", "plain_report.rb"), Autoloading.pending_autoload(:PlainReport)
    assert_equal xml, Autoloading.pending_autoload(:XmlWriter)
    refute Object.const_defined?(:XMLWriter, false)

    # Run it again and you get the next one, having made no progress on the
    # first: N misnamed files cost N boots. What eager_load genuinely buys is
    # WHEN the failure lands — at boot, not on the first request that happens to
    # touch that constant.
    second = assert_raises(Zeitwerk::NameError) { loader.eager_load }
    assert_equal "expected file #{xml} to define constant XmlWriter, but didn't", second.message
    assert Autoloading.loaded?(:PlainReport)
  end

  def test_all_expected_cpaths_lists_every_expected_constant_without_loading
    dir = Autoloading.tree("audit")
    loader = Autoloading.loader_for("audit")

    # This is the call that does what eager_load is wrongly credited with: the
    # complete map of path to the constant Zeitwerk will demand there, in one
    # pass, for the whole tree. Diff it against your files and every naming
    # mismatch is visible at once.
    expected = {
      dir => "Object",
      File.join(dir, "oauth_token.rb") => "OauthToken",
      File.join(dir, "sql_builder.rb") => "SqlBuilder",
      File.join(dir, "user_record.rb") => "UserRecord"
    }
    assert_equal expected, loader.all_expected_cpaths

    # Nothing was loaded to produce it — the autoloads are all still pending,
    # so it is safe to call on a tree you already know is broken.
    assert_equal File.join(dir, "oauth_token.rb"), Autoloading.pending_autoload(:OauthToken)
    assert_equal File.join(dir, "sql_builder.rb"), Autoloading.pending_autoload(:SqlBuilder)
    assert_equal File.join(dir, "user_record.rb"), Autoloading.pending_autoload(:UserRecord)
    refute Object.const_defined?(:OAuthToken, false)
    refute Object.const_defined?(:SQLBuilder, false)
  end

  def test_a_directory_can_be_managed_by_only_one_loader
    dir = Autoloading.tree("claimed")
    Autoloading.loader_for("claimed")
    assert Widget

    # The claim is taken in push_dir, before setup, and it is process-wide. This
    # is why a test suite that builds a fresh loader per example has to give
    # each one its own tree, and why this seed does.
    err = assert_raises(Zeitwerk::Error) { Zeitwerk::Loader.new.push_dir(dir) }
    refute_kind_of ::NameError, err
    assert_includes err.message, "wants to manage directory"
    assert_includes err.message, dir
  end

  def test_zeitwerk_is_pure_ruby_with_no_dependencies
    # Why the newest release could be pinned here while other Ruby seeds in this
    # repo had to hold back: zeitwerk ships no C extension and no runtime
    # dependency, so it installs on an image with no compiler.
    spec = Gem.loaded_specs.fetch("zeitwerk")

    assert_equal "2.8.3", spec.version.to_s
    assert_empty spec.extensions
    assert_empty spec.dependencies.select { |d| d.type == :runtime }
    assert_equal Zeitwerk::VERSION, spec.version.to_s
  end
end
