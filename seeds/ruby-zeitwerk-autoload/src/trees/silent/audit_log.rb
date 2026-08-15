# A file that defines no constant at all. In a real codebase this is usually a
# file left holding only requires, monkey patches or a constant that moved out,
# and it fails exactly like a misspelled class name does.
#
# The assignment writes to a thread local instead of a constant so the contract
# can show the body really did run before Zeitwerk complained.
Thread.current[:csx_audit_log_file_ran] = true
