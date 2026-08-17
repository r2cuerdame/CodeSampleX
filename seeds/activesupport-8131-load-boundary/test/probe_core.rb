require "json"
require "active_support"

puts JSON.generate(
  time_with_zone_constant: ActiveSupport.const_defined?(:TimeWithZone, false),
  time_zone_constant: ActiveSupport.const_defined?(:TimeZone, false),
  time_with_zone_autoload: !ActiveSupport.autoload?(:TimeWithZone).nil?,
  time_zone_autoload: !ActiveSupport.autoload?(:TimeZone).nil?,
  time_zone_method: Time.respond_to?(:zone),
  find_zone_method: Time.respond_to?(:find_zone),
  current_method: Time.respond_to?(:current)
)
