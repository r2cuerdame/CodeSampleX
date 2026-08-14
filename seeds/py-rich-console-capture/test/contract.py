import io
import os
import sys
from importlib.metadata import version
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import rich
import rich.errors
from rich.console import Console
from rich.highlighter import ReprHighlighter
from rich.markup import escape
from rich.style import Style
from rich.table import Table
from rich.text import Text

from src.report import (ENVIRONMENT_INPUTS, Both, Checklist, Duration, Job,
                        capture_to_string, pinned_environment,
                        render_to_string, status_table, string_console)

# Every assertion below is about what rich reads from its environment, so
# the environment is cleared first. A suite that skips this step passes on
# a laptop and fails in a runner that exports COLUMNS or FORCE_COLOR.
for key in ENVIRONMENT_INPUTS:
    os.environ.pop(key, None)

# The width fallback asserted further down is only the fallback when no
# standard stream is a terminal. That is true here - the sandbox has no
# tty - and this check turns a surprising width into a clear message if
# somebody re-runs the contract with docker -t.
assert not any(stream is not None and stream.isatty() for stream in
               (sys.__stdin__, sys.__stdout__, sys.__stderr__))


def plain(**kwargs):
    buffer = io.StringIO()
    return Console(file=buffer, **kwargs), buffer


# --- escape codes are a setting, not a property of the output -----------
# A StringIO is not a terminal, so rich emits nothing but text: markup is
# applied to the text and then thrown away for want of anywhere to put it.
console, buffer = plain()
assert console.is_terminal is False
assert console.color_system is None
console.print("[bold]hello[/bold]")
assert buffer.getvalue() == "hello\n"
assert "\x1b" not in buffer.getvalue()

console, buffer = plain(force_terminal=False)
console.print("[bold]hello[/bold]")
assert buffer.getvalue() == "hello\n"

# The same call with the switch flipped. Same text, different bytes. The
# detected color_system is "standard" only because TERM was cleared above.
console, buffer = plain(force_terminal=True)
assert console.is_terminal is True
assert console.color_system == "standard"
console.print("[bold]hello[/bold]")
assert buffer.getvalue() == "\x1b[1mhello\x1b[0m\n"

# What TERM and COLORTERM decide is which color_system is detected, and
# COLORTERM outranks TERM rather than the other way round. MEASURED, and
# it narrows the usual advice: neither one moves the bytes for bold, which
# is an attribute and not a colour. What makes the assertion above stable
# is clearing the environment, not pinning color_system.
with pinned_environment(TERM="xterm-256color"):
    console, buffer = plain(force_terminal=True)
    assert console.color_system == "256"
    console.print("[bold]hello[/bold]")
    assert buffer.getvalue() == "\x1b[1mhello\x1b[0m\n"
with pinned_environment(TERM="xterm-256color", COLORTERM="truecolor"):
    assert plain(force_terminal=True)[0].color_system == "truecolor"

# color_system is settled in the constructor, while width is read at
# access time (asserted further down). One console therefore follows the
# environment for its width and ignores it for its colours.
console, _ = plain(force_terminal=True)
with pinned_environment(TERM="xterm-256color"):
    assert console.color_system == "standard"
    assert plain(force_terminal=True)[0].color_system == "256"

# Nobody has to set force_terminal for that to happen in CI. FORCE_COLOR
# in the environment turns is_terminal on for a plain file object, which
# is how a log full of escape codes gets produced by code that never asked
# for colour. It is a configuration, and the fix is the same switch.
with pinned_environment(FORCE_COLOR="1"):
    console, buffer = plain()
    assert console.is_terminal is True
    console.print("[bold]hello[/bold]")
    assert buffer.getvalue() == "\x1b[1mhello\x1b[0m\n"

# TTY_COMPATIBLE is read before FORCE_COLOR and "0" beats it, so the fix
# for that log stops working in a runner that exports it, with nothing
# said. It also decides is_terminal on its own. This suite clears it for
# the same reason it clears the rest: set to "0" in the environment, it
# would fail the assertion directly above.
with pinned_environment(TTY_COMPATIBLE="0", FORCE_COLOR="1"):
    console, buffer = plain()
    assert console.is_terminal is False
    assert console.color_system is None
    console.print("[bold]hello[/bold]")
    assert buffer.getvalue() == "hello\n"
with pinned_environment(TTY_COMPATIBLE="1"):
    console = plain()[0]
    assert console.is_terminal is True
    assert console.color_system == "standard"

# is_terminal and color_system are two switches, not one. TERM=dumb leaves
# is_terminal True and drops color_system to None, so a "terminal" console
# emits no codes at all.
with pinned_environment(TERM="dumb", FORCE_COLOR="1"):
    console, buffer = plain()
    assert console.is_terminal is True
    assert console.color_system is None
    console.print("[bold]hello[/bold]")
    assert buffer.getvalue() == "hello\n"

# no_color is the switch people reach for, and it is not the one they
# want: it suppresses colour and leaves every other attribute alone, so
# bold still arrives as an escape sequence.
console, buffer = plain(force_terminal=True, no_color=True)
console.print("[bold red]hello[/bold red]")
assert buffer.getvalue() == "\x1b[1mhello\x1b[0m\n"

# color_system=None is the one that means "no escape codes, ever", and it
# holds even with force_terminal=True.
console, buffer = plain(force_terminal=True, color_system=None)
console.print("[bold red]hello[/bold red]")
assert buffer.getvalue() == "hello\n"

# --- a style keeps the codes it rendered the first time -----------------
# MEASURED, and for anyone asserting on exact bytes it outranks every
# switch above: Style stores the codes it rendered in Style._ansi and
# reuses them, and Style.parse returns the same object for the same
# markup, so the first console to render a style fixes its bytes for the
# rest of the process. A truecolor console writes #00aaff as 24-bit, and a
# standard console asked for that colour afterwards repeats those bytes
# instead of downgrading to the nearest of sixteen.
console, buffer = plain(force_terminal=True, color_system="truecolor")
console.print("[#00aaff]x[/#00aaff]")
assert buffer.getvalue() == "\x1b[38;2;0;170;255mx\x1b[0m\n"
console, buffer = plain(force_terminal=True, color_system="standard")
console.print("[#00aaff]x[/#00aaff]")
assert buffer.getvalue() == "\x1b[38;2;0;170;255mx\x1b[0m\n"

# The same thing the other way round, with a colour nothing has rendered
# yet: standard first flattens it to green, and the truecolor console that
# follows is stuck with green.
console, buffer = plain(force_terminal=True, color_system="standard")
console.print("[#00bb44]x[/#00bb44]")
assert buffer.getvalue() == "\x1b[32mx\x1b[0m\n"
console, buffer = plain(force_terminal=True, color_system="truecolor")
console.print("[#00bb44]x[/#00bb44]")
assert buffer.getvalue() == "\x1b[32mx\x1b[0m\n"

# The cache lives on the Style, which is where to look when bytes that
# should differ do not.
style = Style.parse("#00bb44")
assert style._ansi == "32"
assert Style.parse("#00bb44") is style

# --- capture() returns the rendered text --------------------------------
console, buffer = string_console()
with console.capture() as capture:
    console.print("captured")
console.print("after")

# It redirects, it does not tee: nothing written inside the block reached
# the console's own file. Asserting on both is the only way to see that.
assert capture.get() == "captured\n"
assert buffer.getvalue() == "after\n"

# capture() renders, so it captures whatever the console would have
# written - escape codes included. It is not a way to get plain text.
assert capture_to_string("[bold]x[/bold]") == "x\n"
assert capture_to_string("[bold]x[/bold]", color=True) == "\x1b[1mx\x1b[0m\n"

# Nested captures do not nest. The inner block ends the capture that the
# outer block started, so the inner result holds everything printed since
# the outer `with`, and the outer result holds only what came after it.
console, buffer = string_console()
with console.capture() as outer:
    console.print("a")
    with console.capture() as inner:
        console.print("b")
    console.print("c")
assert inner.get() == "a\nb\n"
assert outer.get() == "c\n"

# record=True is the other way out, and export_text clears the recording
# by default, so the second call on the same console returns nothing.
console = Console(file=io.StringIO(), record=True, force_terminal=True,
                  color_system="standard", legacy_windows=False)
console.print("[bold]rec[/bold]")
assert console.export_text() == "rec\n"
assert console.export_text() == ""
console.print("[bold]rec[/bold]")
assert console.export_text(styles=True, clear=False) == "\x1b[1mrec\x1b[0m\n"
assert console.export_text(styles=True, clear=False) == "\x1b[1mrec\x1b[0m\n"

# --- width: 80 is a fallback, and COLUMNS overrides it ------------------
# No stream is a terminal here, so os.get_terminal_size has nothing to
# answer with and rich falls back to 80 by 25.
try:
    os.get_terminal_size()
except OSError:
    pass
else:
    raise AssertionError("expected no terminal size in the sandbox")

console, _ = plain()
assert console.width == 80
assert console.size == (80, 25)

# COLUMNS wins over the detected size, and width is read at access time,
# not at construction: the same console object answers differently after
# the environment changes underneath it.
with pinned_environment(COLUMNS="120"):
    assert console.width == 120
    assert plain()[0].width == 120
assert console.width == 80

# An explicit width beats COLUMNS, which is why string_console sets one.
with pinned_environment(COLUMNS="120"):
    assert plain(width=60)[0].width == 60

# MEASURED, against the usual advice: a Table does not fill the console.
# It measures its content and stops, so this one is 17 columns wide inside
# a 40, an 80 and a 120 column console. Pinning the console width does not
# make a small table stable - it already was, and the rows are the proof.
table_rows = [("alpha", 1), ("beta", 22)]
narrow = render_to_string(status_table(table_rows), width=40)
assert narrow.splitlines() == [
    "┏━━━━━━━┳━━━━━━━┓",
    "┃ name  ┃ count ┃",
    "┡━━━━━━━╇━━━━━━━┩",
    "│ alpha │ 1     │",
    "│ beta  │ 22    │",
    "└───────┴───────┘",
]
assert render_to_string(status_table(table_rows), width=80) == narrow
assert render_to_string(status_table(table_rows), width=120) == narrow
assert {len(line) for line in narrow.splitlines()} == {17}

# What does follow the console width is expand=True, and content too wide
# to fit. Those are the renders that move when COLUMNS moves, and the only
# ones an explicit width= actually pins.
expanding = Table("name", "count", expand=True)
expanding.add_row("alpha", "1")
for width in (40, 80, 120):
    rendered = render_to_string(expanding, width=width)
    assert {len(line) for line in rendered.splitlines()} == {width}

assert [len(line) for line in
        render_to_string("z" * 100, width=80).splitlines()] == [80, 20]
assert [len(line) for line in
        render_to_string("z" * 100, width=120).splitlines()] == [100]

# --- markup is on by default, and it rewrites user data -----------------
assert render_to_string("[bold]x[/bold]") == "x\n"
assert render_to_string("[bold]x[/bold]", color=True) == "\x1b[1mx\x1b[0m\n"

console, buffer = string_console()
console.print("[bold]x[/bold]", markup=False)
assert buffer.getvalue() == "[bold]x[/bold]\n"

console, buffer = string_console(markup=False)
console.print("[bold]x[/bold]")
assert buffer.getvalue() == "[bold]x[/bold]\n"

# Two shapes of silent deletion, for text that came from somewhere else:
# a generic type name loses its parameter, and a sentence in square
# brackets renders as an empty line. Neither raises anything.
assert render_to_string("list[int]") == "list\n"
assert render_to_string("[not a tag]") == "\n"

# An unknown style is swallowed without an error, taking the tags with it,
# and it is swallowed on a console that emits codes too - the name does
# not have to resolve for the text to survive and the tags to vanish.
assert render_to_string("[nosuchstyle]hi[/nosuchstyle]") == "hi\n"
console, buffer = string_console(color=True)
console.print("[nosuchstyle]hi[/nosuchstyle]")
assert buffer.getvalue() == "hi\n"

# And a stray closing bracket raises, so printing user text can end the
# process. MarkupError descends from ConsoleError, not from ValueError, so
# the except clause a defensive caller already wrote does not catch it.
try:
    render_to_string("user said [/wat]")
except rich.errors.MarkupError as error:
    assert "doesn't match any open tag" in str(error)
    assert not isinstance(error, ValueError)
    assert isinstance(error, rich.errors.ConsoleError)
else:
    raise AssertionError("stray closing tag did not raise")

# The two fixes: escape() at the point of interpolation, or markup=False.
assert render_to_string(escape("user said [/wat]")) == "user said [/wat]\n"
console, buffer = string_console(markup=False)
console.print("user said [/wat]")
assert buffer.getvalue() == "user said [/wat]\n"

# Markup is not the only rewrite. Emoji codes are substituted by default,
# which is the same class of surprise for logged text.
assert render_to_string("build :rocket: done") == "build \U0001f680 done\n"
console, buffer = string_console(emoji=False)
console.print("build :rocket: done")
assert buffer.getvalue() == "build :rocket: done\n"

# A Text object is never parsed for markup, which is what makes it the
# right container for text you did not write.
console, buffer = string_console()
console.print(Text("[bold]x[/bold]"))
assert buffer.getvalue() == "[bold]x[/bold]\n"

# --- what console.print does with an object -----------------------------
# __rich__ first. It returns a string here, and that string IS parsed for
# markup, so an object is not a way to escape the hazard above.
assert render_to_string(Duration(0.5)) == "0.50s\n"

# MEASURED: a string from __rich__ takes the whole string pipeline, which
# includes the highlighter, and the highlighter cuts the style run in two.
# "0." matches its number pattern and gets bold on top of the green, so
# "0.50s" arrives as two escape runs instead of the one the markup asked
# for. Any golden test on a __rich__ object has to expect this.
assert (render_to_string(Duration(0.5), color=True)
        == "\x1b[1;32m0.\x1b[0m\x1b[32m50s\x1b[0m\n")
assert (render_to_string(Duration(2.0), color=True)
        == "\x1b[1;31m2.\x1b[0m\x1b[31m00s\x1b[0m\n")

# highlight=False leaves the markup's own style alone, one run as written.
console, buffer = string_console(color=True)
console.print(Duration(0.5), highlight=False)
assert buffer.getvalue() == "\x1b[32m0.50s\x1b[0m\n"

# __rich_console__ next, for objects that need the console or its options
# and emit more than one renderable. max_width is the width the object is
# being rendered into, so it tracks the console.
assert render_to_string(Checklist(["a", "b"]), width=40) == (
    "[x] a\n[x] b\n2 done, width 40\n")
assert render_to_string(Checklist(["a", "b"]), width=80).endswith(
    "width 80\n")

# Both defined: __rich__ wins, because rich_cast runs before anything
# looks for a renderable, and it returns a string that is not one.
assert render_to_string(Both(0.5)) == "0.50s\n"

# __rich__ is looked up on the instance, not only on the type - unlike
# every dunder Python itself dispatches. Patching one object works.
job = Job("alpha")
job.__rich__ = lambda: "patched"
assert render_to_string(job) == "patched\n"

# MEASURED, and it contradicts the usual summary of this API: an object
# with no rich hooks is rendered with str(), not repr(). The two are the
# same for most objects, which is why "it uses repr" survives as folklore
# - here they differ, and str wins.
assert str(Job("alpha")) != repr(Job("alpha"))
assert render_to_string(Job("alpha")) == "job alpha\n"

# repr does appear, one level down: a container is pretty-printed, and the
# pretty printer uses repr on its items.
assert render_to_string([Job("alpha")]) == "[Job(name='alpha')]\n"

# The str() branch skips markup and emoji parsing entirely, so an object
# is safe from the injection above in a way a bare string is not.
assert render_to_string(Job("[bold]x[/bold]")) == "job [bold]x[/bold]\n"
assert render_to_string(Job(":rocket:")) == "job :rocket:\n"

# The highlighter is the one rewrite it does not skip: the number in the
# same string is styled even though the markup in it was left alone.
console, buffer = string_console(color=True)
console.print(Job("42 items"))
assert buffer.getvalue() == "job \x1b[1;36m42\x1b[0m items\n"
console, buffer = string_console(color=True)
console.print(Job("42 items"), highlight=False)
assert buffer.getvalue() == "job 42 items\n"

# --- the highlighter rewrites numbers, unless you turn it off -----------
assert render_to_string(123, color=True) == "\x1b[1;36m123\x1b[0m\n"

console, buffer = string_console(color=True)
console.print(123, highlight=False)
assert buffer.getvalue() == "123\n"

console, buffer = string_console(color=True, highlight=False)
console.print(123)
assert buffer.getvalue() == "123\n"

# It is a regex pass over the text, not a rule about the argument, so a
# number inside a sentence is styled too, and so is the shape of a repr.
console, buffer = string_console(color=True)
console.print("the value is 42 ok")
assert buffer.getvalue() == "the value is \x1b[1;36m42\x1b[0m ok\n"

console, buffer = string_console(color=True)
console.print([1])
assert buffer.getvalue() == "\x1b[1m[\x1b[0m\x1b[1;36m1\x1b[0m\x1b[1m]\x1b[0m\n"

# MEASURED, and it narrows the claim: on a console that emits no codes,
# highlight=False changes nothing at all, because the highlighter still
# runs and its spans have nowhere to go. Turning it off is only visible
# where colour is.
assert render_to_string(123) == "123\n"
console, buffer = string_console(highlight=False)
console.print(123)
assert buffer.getvalue() == "123\n"
assert ReprHighlighter()("123").spans[0].style == "repr.number"

# The version has to come from importlib.metadata: rich exports no
# __version__, so a test helper that prints one raises AttributeError.
assert not hasattr(rich, "__version__")
print("contract ok: rich", version("rich"), "console width fallback",
      Console(file=io.StringIO()).width)
