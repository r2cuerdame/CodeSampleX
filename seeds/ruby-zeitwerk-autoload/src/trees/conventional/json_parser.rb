# json_parser.rb -> JsonParser. Zeitwerk::Inflector camelizes on underscores
# only, so every acronym comes out capitalised like an ordinary word.
class JsonParser
  # Set at load time so the contract can prove the file body ran only when the
  # constant was first referenced, not at setup.
  LOADED_AT = "file body evaluated"

  def self.parse(text) = text
end
