import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from click.testing import CliRunner

from src.cli import greet

runner = CliRunner()

# CliRunner calls the command in this process: no subprocess, no PATH, and
# the exit code and output are both returned rather than having to be
# scraped from a pipe.
result = runner.invoke(greet, ["codesamplex", "--count", "2"])
assert result.exit_code == 0, result.output
assert result.output == "hello codesamplex\nhello codesamplex\n", repr(result.output)

result = runner.invoke(greet, ["csx", "--shout"])
assert result.exit_code == 0
assert result.output.strip() == "HELLO CSX"

# Usage errors exit 2 — click's convention, distinct from an app error.
result = runner.invoke(greet, ["csx", "--count", "not-a-number"])
assert result.exit_code == 2, result.output
assert "not a valid integer" in result.output.lower(), result.output

# A missing required argument is also a usage error, not a crash.
result = runner.invoke(greet, [])
assert result.exit_code == 2
assert "missing argument" in result.output.lower(), result.output

print("CONTRACT PASS: click ran in-process and reported usage errors as exit 2")
