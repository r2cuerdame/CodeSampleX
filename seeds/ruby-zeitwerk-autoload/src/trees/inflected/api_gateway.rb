# Same shape as the http_client.rb / HTTPClient trap, fixed the supported way:
# the loader in the contract overrides the inflection for this basename, so
# Zeitwerk expects APIGateway and this file is correct rather than broken.
class APIGateway
  def self.route(path) = path
end
