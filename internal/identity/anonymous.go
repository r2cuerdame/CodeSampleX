package identity

// AnonymousClientID is a stable, server-scoped pseudonym for free-client
// analytics. Unlike rotating evidence IDs it is intentionally linkable across
// days. Neither the seed nor the peer signing key leaves this machine.
func (id *Identity) AnonymousClientID(origin string) string {
	return id.derive("anonymous-client|v1|"+origin, 64)
}
