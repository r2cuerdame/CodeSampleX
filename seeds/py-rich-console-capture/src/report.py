"""Rendering rich to a string, because a terminal is not a test fixture.

Console writes to whatever file you hand it, so pointing it at a StringIO
is all it takes to get the output into a variable. What ends up in that
variable is decided by three inputs that are inferred from the environment
unless you set them, which is why the same code prints clean text on a
laptop and escape-code soup in CI:

  is_terminal  - whether rich believes it is driving a terminal. Decided
                 in this order: force_terminal, then TTY_COMPATIBLE, then
                 FORCE_COLOR, and only then file.isatty(). The environment
                 is consulted before the file, which is why a StringIO can
                 report True. TERM is not read here at all.
  color_system - which codes it may emit: "standard", "256", "truecolor",
                 or None for none at all. Detected from COLORTERM first
                 and TERM second, and None when TERM is dumb. It is
                 settled in the constructor, unlike width, so changing the
                 environment afterwards moves an existing Console's width
                 and not its colours.
  width        - asked of the terminal, overridden by the COLUMNS
                 environment variable, and 80 when neither answers.

Those are two independent switches, not one. A dumb terminal is
is_terminal True with color_system None, and no_color=True still emits the
bold code, because bold is not a colour. If you want "no escape codes,
ever", the switch is color_system=None.

string_console pins all three with public Console arguments. Console also
accepts a private _environ mapping, which every one of those lookups goes
through and which would pin the environment instead, but the underscore
is a warning and width= plus force_terminal= cover it.

The output of a Console is not the input you fed it. Markup, emoji codes
and the repr highlighter all rewrite text on the way through, and which of
them run depends on what type you passed. test/contract.py measures the
full dispatch order; the short version is that a str argument is parsed
for markup and a printed object is not.
"""

from __future__ import annotations

import io
import os
from contextlib import contextmanager
from typing import Iterable, Iterator

from rich.console import Console, ConsoleOptions, RenderResult
from rich.table import Table
from rich.text import Text

# Every variable a Console reads when you leave it to guess, outside
# Jupyter - JUPYTER_COLUMNS and JUPYTER_LINES are read only there. A test
# that does not neutralise these is testing the machine it runs on, and
# the four that a list written from memory leaves out are the ones that
# bite: TTY_COMPATIBLE is checked before FORCE_COLOR and "0" beats it,
# COLORTERM outranks TERM when the colour system is detected,
# TTY_INTERACTIVE sets is_interactive, and UNICODE_VERSION chooses the
# table rich measures character widths against.
ENVIRONMENT_INPUTS = ("COLUMNS", "LINES", "TERM", "NO_COLOR", "FORCE_COLOR",
                      "TTY_COMPATIBLE", "COLORTERM", "TTY_INTERACTIVE",
                      "UNICODE_VERSION")


@contextmanager
def pinned_environment(**overrides: str) -> Iterator[None]:
    """Run a block with rich's environment inputs cleared, then restored.

    Values passed as keyword arguments are set instead of cleared, which is
    how the contract reproduces a CI runner that exports FORCE_COLOR.
    """
    saved = {key: os.environ.get(key) for key in ENVIRONMENT_INPUTS}
    saved.update({key: os.environ.get(key) for key in overrides})
    try:
        for key in ENVIRONMENT_INPUTS:
            os.environ.pop(key, None)
        os.environ.update(overrides)
        yield
    finally:
        for key, value in saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value


def string_console(
    *, width: int = 80, color: bool = False, **overrides: object
) -> tuple[Console, io.StringIO]:
    """A Console writing to a string, with nothing left to the environment.

    color=False is not the same thing as force_terminal=False alone:
    force_terminal decides whether rich thinks it is on a terminal, and
    color_system decides whether it may emit codes. Setting both is what
    makes the output identical on a developer's tty and in a CI pipe.

    width= is what pins a Table. It beats COLUMNS, which is the variable
    that quietly re-wraps golden output when a CI image sets it.
    """
    buffer = io.StringIO()
    console = Console(
        file=buffer,
        width=width,
        force_terminal=color,
        color_system="standard" if color else None,
        # Left alone this is detected rather than off: it turns on for a
        # Windows console without VT support, and legacy mode substitutes
        # the box-drawing characters and clamps colours to the sixteen the
        # old console had. A test asserting on box characters has to say
        # which set it means.
        legacy_windows=False,
        **overrides,
    )
    return console, buffer


def render_to_string(renderable: object, *, width: int = 80,
                     color: bool = False) -> str:
    """Render one thing and return the text, with no console left over."""
    console, buffer = string_console(width=width, color=color)
    console.print(renderable)
    return buffer.getvalue()


def capture_to_string(renderable: object, *, width: int = 80,
                      color: bool = False) -> str:
    """The same thing through console.capture().

    capture() redirects rather than tees: the console's file receives
    nothing while a capture is open. It exists for the case where you do
    not own the Console - a library that prints to its own global console
    can be captured this way, where swapping in a StringIO cannot.
    """
    console, _ = string_console(width=width, color=color)
    with console.capture() as capture:
        console.print(renderable)
    return capture.get()


def status_table(rows: Iterable[tuple[str, int]]) -> Table:
    """A small table, deliberately narrower than any console width.

    A Table sizes itself to its content and stops. It only follows the
    console width when it is told to (expand=True) or when the content
    does not fit, which is the difference between "the width is stable"
    and "the width does not depend on the environment".
    """
    table = Table("name", "count")
    for name, count in rows:
        table.add_row(name, str(count))
    return table


class Duration:
    """Renders through __rich__, which returns a string with markup in it.

    __rich__ is consulted before anything else, and a string it returns is
    parsed for markup just like a string you passed to print yourself. An
    object is not automatically the safe way to print untrusted data; a
    __rich__ that interpolates user text has the same hole as print().
    """

    def __init__(self, seconds: float) -> None:
        self.seconds = seconds

    def __repr__(self) -> str:
        return f"Duration({self.seconds!r})"

    def __rich__(self) -> str:
        style = "red" if self.seconds > 1 else "green"
        return f"[{style}]{self.seconds:.2f}s[/{style}]"


class Checklist:
    """Renders through __rich_console__, which yields more than one line.

    __rich_console__ is the interface for something that has to look at
    the console or its options - the width it is being rendered into, for
    instance - and can emit several renderables. It is only reached if the
    object has no __rich__.
    """

    def __init__(self, items: Iterable[str]) -> None:
        self.items = list(items)

    def __repr__(self) -> str:
        return f"Checklist({self.items!r})"

    def __rich_console__(self, console: Console,
                         options: ConsoleOptions) -> RenderResult:
        for item in self.items:
            yield Text(f"[x] {item}")
        yield Text(f"{len(self.items)} done, width {options.max_width}")


class Job:
    """No rich hooks at all, and __str__ differs from __repr__ on purpose.

    Which one rich uses is the assertion in test/contract.py, and the
    answer is not the one the documentation's phrasing suggests.
    """

    def __init__(self, name: str) -> None:
        self.name = name

    def __str__(self) -> str:
        return f"job {self.name}"

    def __repr__(self) -> str:
        return f"Job(name={self.name!r})"


class Both(Duration):
    """Defines both hooks, to settle which one wins."""

    def __rich_console__(self, console: Console,
                         options: ConsoleOptions) -> RenderResult:
        yield Text("from __rich_console__")
