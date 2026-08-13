"""A click command and the runner that tests it in-process."""

import click


@click.command()
@click.argument("name")
@click.option("--count", default=1, type=int, help="how many times to greet")
@click.option("--shout/--no-shout", default=False)
def greet(name, count, shout):
    """Greet NAME COUNT times."""
    line = "hello %s" % name
    if shout:
        line = line.upper()
    for _ in range(count):
        click.echo(line)
