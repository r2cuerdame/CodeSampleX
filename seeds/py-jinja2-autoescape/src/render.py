"""jinja2 escaping, which is off unless you turn it on."""

from jinja2 import Environment, select_autoescape
from markupsafe import Markup


def render(template, autoescape=False, **values):
    """Render a template string.

    jinja2's default Environment has autoescape=False. Flask turns it on for
    .html templates, which is why the same code is safe in a Flask app and
    an injection hole in a script that builds its own Environment. There is
    no warning either way — the output simply contains whatever was passed.
    """
    env = Environment(autoescape=autoescape)
    return env.from_string(template).render(**values)


def render_for(filename, template, **values):
    """Escape based on the file type, the way Flask does."""
    env = Environment(autoescape=select_autoescape(default_for_string=False))
    env.policies["ext.i18n.trimmed"] = False
    return env.from_string(template).render(**values)


def trusted(html):
    """Mark HTML as already-safe so escaping leaves it alone."""
    return Markup(html)
