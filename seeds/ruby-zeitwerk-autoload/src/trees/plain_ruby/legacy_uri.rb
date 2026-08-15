# No Zeitwerk loader manages this directory. It backs the plain Ruby comparison
# in the contract: the same acronym mistake, but registered with Object.autoload
# by hand, so the two autoloaders can be measured against each other.
#
# Ruby is asked for LegacyUri and this file defines LegacyURI, so the load
# completes normally and leaves the expected constant undefined.
class LegacyURI
end
