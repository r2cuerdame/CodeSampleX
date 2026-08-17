require "json"
require "open3"
require "rbconfig"

ROOT = File.expand_path("..", __dir__)

def assert(condition, message)
  raise message unless condition
end

def run_probe(name)
  path = File.join(__dir__, name)
  stdout, stderr, status = Open3.capture3(RbConfig.ruby, path)
  assert(status.success?, "#{name} failed: #{stderr}")
  JSON.parse(stdout, symbolize_names: true)
end

core = run_probe("probe_core.rb")
assert(core == {
  time_with_zone_constant: false,
  time_zone_constant: false,
  time_with_zone_autoload: false,
  time_zone_autoload: false,
  time_zone_method: false,
  find_zone_method: false,
  current_method: false
}, "require active_support unexpectedly loaded time-zone APIs: #{core.inspect}")

time_only = run_probe("probe_time_only.rb")
assert(time_only[:time_with_zone_constant], "active_support/time did not define TimeWithZone")
assert(time_only[:time_zone_constant], "active_support/time did not define TimeZone")
assert(!time_only[:isolated_execution_state], "active_support/time unexpectedly loaded IsolatedExecutionState")
assert(time_only[:time_zone_method], "active_support/time did not add Time.zone")
assert(time_only[:find_zone_method], "active_support/time did not add Time.find_zone")
assert(time_only[:current_method], "active_support/time did not add Time.current")
assert(time_only.dig(:assignment_error, :class) == "NameError", "Time.zone= unexpectedly succeeded")
assert(time_only.dig(:assignment_error, :name) == "IsolatedExecutionState",
       "unexpected missing constant: #{time_only[:assignment_error].inspect}")
assert(time_only.dig(:assignment_error, :message).include?("ActiveSupport::IsolatedExecutionState"),
       "NameError did not identify the ActiveSupport constant: #{time_only[:assignment_error].inspect}")

combined = run_probe("probe_combined.rb")
assert(combined == {
  time_with_zone_constant: true,
  isolated_execution_state: true,
  time_zone_method: true,
  value_class: "ActiveSupport::TimeWithZone",
  value_zone: "America/New_York",
  value_offset: -14_400,
  value_utc: "2026-07-04 16:30:00"
}, "combined requires produced unexpected TimeWithZone behavior: #{combined.inspect}")

puts "ActiveSupport load-boundary contract passed"
