# The trap. Ruby style says HTTPClient, Zeitwerk::Inflector derives HttpClient
# from this filename, and nothing warns you until something references it.
#
# Deliberately wrong: this file is the failure case the contract measures.
class HTTPClient
  def self.get(_url) = :ok
end
