package web

// The read API.
//
// The features page described the CLI and the MCP tools and never
// mentioned that the network answers HTTP directly. Everything the site
// renders is served from these endpoints, so a reader who wants the evidence
// without either the CLI or an agent had no way to learn they exist.
//
// Only the read half is listed. The write endpoints — evidence batches,
// sample publication, verification jobs, the device-code flow — are the
// CLI's business: they need a seeder identity or a worker token, and
// documenting them as though anyone can call them would invite requests that
// can only be refused.

// apiEndpoint is one route a reader can call without credentials.
type apiEndpoint struct {
	Method string
	Path   string
	// What names the route in one line. It is deliberately not translated,
	// for the reason findings are not: the paths, the field names and the
	// shapes are the thing itself, and a translation that drifts from them
	// is worse than none. The page around it is translated.
	What string
}

// publicReadAPI is the list as registered in internal/httpapi/api.go. It is
// written out rather than derived because the router carries the write and
// worker routes in the same table, and a page that enumerated them would
// promise access it cannot grant.
func publicReadAPI() []apiEndpoint {
	return []apiEndpoint{
		{"POST", "/v1/search",
			"Search the network: packages, symbols, error codes. The same query the MCP tool and the site's own search run."},
		{"GET", "/v1/registry/packages/{purl}",
			"One package's compatibility snapshot, by PURL. Percent-encode the slash: pkg:npm%2Faxios@1.12.0."},
		{"GET", "/v1/registry/symbols/{ecosystem}/{name}/{symbol}",
			"One API's snapshot: where it ran, where it failed, at what evidence level."},
		{"GET", "/v1/shards/{ecosystem}/{name}/{n}",
			"The offline shard a client syncs, so a machine can answer without asking again. ETag-cached."},
		{"GET", "/v1/samples/{sampleId}",
			"A published sample's manifest: its case, its contract, its verification receipts."},
		{"GET", "/v1/samples/{sampleId}/artifact",
			"That sample's files. The id is the hash of the contents, so the bytes verify themselves."},
		{"GET", "/v1/peers/for-sample/{sampleId}",
			"Peers holding that sample, for fetching it without this server."},
		{"GET", "/v1/wanted",
			"What the network has been asked for and has no sample for yet."},
		{"GET", "/v1/adapters",
			"The ecosystems and lockfiles the scanner reads."},
		{"GET", "/v1/stats",
			"Observation, sample and package counts — what the front page's tiles are drawn from."},
	}
}
