# Zeitwerk: what the file-name-to-constant rule really is, and what happens the
# moment you break it.
#
# Zeitwerk is the autoloader Rails has shipped with since Rails 6, and it is
# what `require` is replaced by in a Zeitwerk-managed project. It is worth
# knowing exactly how it decides what a file must contain.
#
# It never scans a file to find out what is inside: it derives the constant
# name from the path with an inflector and installs a Ruby autoload for it. The
# contract is one-directional and total — the file MUST define exactly the
# constant Zeitwerk decided on, or referencing it fails.
#
# The rule, measured rather than quoted: Zeitwerk::Inflector#camelize splits the
# basename on underscores and capitalises each part. So http_client.rb must
# define HttpClient, not HTTPClient; json_parser.rb must define JsonParser; and
# no amount of Ruby style guidance about acronyms changes that unless you say so
# through an inflector.
#
# Three things about the failure surprise people, and all three are asserted in
# the contract:
#
#   1. The Zeitwerk::NameError names the FILE and the constant Zeitwerk EXPECTED.
#      It does not name the constant your file actually defined — Zeitwerk has
#      no idea what that was, it only checked whether its own name got defined.
#   2. Your file was loaded anyway. Its body ran, its side effects happened, and
#      the wrongly named constant is now sitting in the namespace. Only the
#      expected name is missing.
#   3. Once that has happened the autoload is gone, so the SECOND reference
#      raises a plain ::NameError "uninitialized constant" with none of the
#      diagnostic text. A retry, a rescue, or a second test hitting the same
#      constant sees the useless error, not the good one. Zeitwerk removes that
#      autoload itself, and it has to: it raises from inside the require its own
#      autoload triggered, and Ruby keeps an autoload registered when the
#      require ends in an exception. The contract measures both halves against
#      plain Object.autoload, because "Zeitwerk is stricter than Ruby here" is
#      only true for that raising shape — for a file that simply defines the
#      wrong constant, plain Ruby ends up in exactly the same state.
#
# eager_load is worth reaching for, but not for the reason usually given: it
# turns a naming bug into a boot failure instead of a request failure. It is
# NOT a naming audit. It aborts on the first offending file in sorted order and
# every later mismatch stays invisible until you fix that one — measured in the
# contract, N broken files take N runs. The API that does list every constant
# Zeitwerk will demand, in one call and without loading anything, is
# all_expected_cpaths.
#
# Layout note that is itself a trap: a directory may be managed by exactly one
# loader per process, and the claim is taken in push_dir, before setup. That is
# why every scenario below gets its own root directory; pointing a second loader
# at a directory another loader already holds raises Zeitwerk::Error.

require "zeitwerk"

module Autoloading
  TREES = File.expand_path("trees", __dir__)

  # Absolute path of one of the sample trees. Zeitwerk reports absolute paths in
  # its errors, so the contract builds its expectations the same way instead of
  # matching on a fragment.
  def self.tree(name) = File.join(TREES, name)

  def self.file(tree_name, basename) = File.join(tree(tree_name), basename)

  # A loader over exactly one sample tree. Each tree is used by one test only,
  # because push_dir refuses a directory another live loader already registered.
  def self.loader_for(tree_name, inflections: nil)
    loader = Zeitwerk::Loader.new
    loader.inflector.inflect(inflections) if inflections
    loader.push_dir(tree(tree_name))
    loader.setup
    loader
  end

  # Has this constant been loaded yet? const_defined? cannot answer that: Ruby
  # reports a constant with a pending autoload as defined, which is what makes
  # "it is defined, so it loaded" such a reliable way to fool yourself. autoload?
  # returns the file while the autoload is pending and nil once it has run.
  def self.pending_autoload(cname) = Object.autoload?(cname)

  def self.loaded?(cname)
    Object.const_defined?(cname, false) && Object.autoload?(cname).nil?
  end
end
