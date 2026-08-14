"""The same constant as clock.DEFAULT_START, evaluated at a different moment.

test/contract.py imports this module for the first time from inside a frozen
block. The code is unremarkable; the point is that the value it keeps for the
rest of the process is decided by whichever test happened to import it first.
In a real suite that is decided by collection order.
"""

from datetime import datetime

IMPORTED_AT = datetime.now()
