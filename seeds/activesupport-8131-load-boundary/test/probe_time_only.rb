require "json"
require "active_support/time"

error = begin
  Time.zone = "America/New_York"
  nil
rescue NameError => caught
  {
    class: caught.class.name,
    name: caught.name.to_s,
    message: caught.message.lines.first.strip
  }
end

puts JSON.generate(
  time_with_zone_constant: ActiveSupport.const_defined?(:TimeWithZone, false),
  time_zone_constant: ActiveSupport.const_defined?(:TimeZone, false),
  isolated_execution_state: ActiveSupport.const_defined?(:IsolatedExecutionState, false),
  time_zone_method: Time.respond_to?(:zone),
  find_zone_method: Time.respond_to?(:find_zone),
  current_method: Time.respond_to?(:current),
  assignment_error: error
)
