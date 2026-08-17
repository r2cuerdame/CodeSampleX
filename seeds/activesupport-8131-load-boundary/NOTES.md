# ActiveSupport 8.1.3.1 time loading boundary

This sample measures three clean Ruby processes so that one `require` cannot
hide what another one loaded.

- `require "active_support"` does not define `ActiveSupport::TimeZone` or
  `ActiveSupport::TimeWithZone`, and it does not add `Time.zone`,
  `Time.find_zone`, or `Time.current`.
- `require "active_support/time"` alone adds those time APIs, but it does not
  load `ActiveSupport::IsolatedExecutionState`. Assigning `Time.zone` therefore
  raises `NameError` in ActiveSupport 8.1.3.1.
- Requiring `active_support` first and `active_support/time` second loads the
  missing state and constructs a working `ActiveSupport::TimeWithZone`.

The successful probe uses a summer date in `America/New_York` and checks the
class, named zone, UTC offset, and converted UTC instant. The verifier runs the
contract from the pinned Bundler lock in the Ruby 3 Debian image.
