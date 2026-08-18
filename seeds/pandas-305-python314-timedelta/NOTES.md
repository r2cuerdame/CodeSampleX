# pandas 3.0.5 on CPython 3.14: Timedelta construction

pandas 3.0.4 wheels could terminate CPython 3.14 with a segmentation fault
when constructing a `Timedelta`. The 3.0.5 release rebuilt the affected wheels
against a compatible NumPy version.

The contract runs the three minimal constructor forms reported in the upstream
regression and asserts their exact 28-day value. Reaching any assertion proves
the interpreter survived; the same process also checks a negative duration so
the contract is not limited to one positive constant.

The dependency closure is pinned for the Python 3.14 Alpine verifier. Resolve
is networked and script-free; the contract runs in a fresh network-disabled
container.
