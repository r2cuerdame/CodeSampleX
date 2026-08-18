# SQLAlchemy 2.0.44 detached ORM state

`Session.close()` detaches objects; it does not erase values already loaded in
their instance state. The default `expire_on_commit=True`, however, expires
loaded state at commit, so closing immediately afterward leaves scalar and
relationship access with no Session available to reload them.

`expire_on_commit=False` preserves scalar state but does not eagerly load lazy
relationships. Use an eager loader such as `selectinload`, or explicitly
`refresh(..., attribute_names=[...])`, before detaching when relationship data
must remain available.
