import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from jinja2 import Environment, select_autoescape

from src.render import render, trusted

hostile = "<script>alert(1)</script>"

# The default: NOT escaped. This is the whole point of the sample.
plain = render("<p>{{ text }}</p>", text=hostile)
assert "<script>" in plain, plain

# Turned on, the same input is escaped.
safe = render("<p>{{ text }}</p>", autoescape=True, text=hostile)
assert "<script>" not in safe, safe
assert "&lt;script&gt;" in safe, safe

# select_autoescape keys off the template name, which is how Flask does it.
env = Environment(autoescape=select_autoescape(enabled_extensions=("html",)))
assert env.autoescape("page.html") is True
assert env.autoescape("message.txt") is False

# Markup is the escape hatch for HTML you actually trust.
kept = render("<p>{{ text }}</p>", autoescape=True, text=trusted("<b>bold</b>"))
assert "<b>bold</b>" in kept, kept

print("CONTRACT PASS: jinja2 escaped only when told to, and honoured Markup")
