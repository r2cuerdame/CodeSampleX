require "json"
require "active_support"
require "active_support/time"

Time.zone = "America/New_York"
value = Time.zone.parse("2026-07-04 12:30:00")

puts JSON.generate(
  time_with_zone_constant: ActiveSupport.const_defined?(:TimeWithZone, false),
  isolated_execution_state: ActiveSupport.const_defined?(:IsolatedExecutionState, false),
  time_zone_method: Time.respond_to?(:zone),
  value_class: value.class.name,
  value_zone: value.time_zone.name,
  value_offset: value.utc_offset,
  value_utc: value.utc.strftime("%Y-%m-%d %H:%M:%S")
)
