require "minitest/autorun"

# The point: these used to come free with the interpreter. They are ordinary
# gems now, so this file loads only because the Gemfile declares them.
# Removing either line reproduces the LoadError an undeclared application
# hits after the upgrade.
require "csv"
require "base64"

class DefaultGemExtractionTest < Minitest::Test
  def test_csv_is_available_because_it_is_declared
    rows = CSV.parse("a,b\n1,2", headers: true)
    assert_equal ["a", "b"], rows.headers
    assert_equal "1", rows.first["a"]
  end

  def test_base64_round_trips_and_strict_refuses_newlines
    assert_equal "codesamplex", Base64.decode64(Base64.encode64("codesamplex"))
    # encode64 inserts newlines every 60 chars; strict_encode64 does not.
    # Tokens built with the non-strict form are the usual breakage.
    assert_equal "Y29kZXNhbXBsZXg=", Base64.strict_encode64("codesamplex")
    long = "a" * 100
    assert_includes Base64.encode64(long), "\n"
    refute_includes Base64.strict_encode64(long), "\n"
  end

  def test_they_are_resolved_gems_now
    assert Gem.loaded_specs.key?("csv"), "csv should be a resolved gem"
    assert Gem.loaded_specs.key?("base64"), "base64 should be a resolved gem"
  end
end
