# Mismatch number one. Sorts first of the three files in this directory, and
# Zeitwerk sorts directory entries itself so eager loading is deterministic
# across file systems: this is the file eager_load stops on.
class CSVWriter
end
