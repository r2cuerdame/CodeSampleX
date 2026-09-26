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
//
// Evidence and aggregated compatibility data served by these endpoints are
// published under CDLA-Permissive-2.0 (DATA_TERMS.md). Published sample source
// defaults to MIT-0.

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
		{"GET", "/v2/search?q=&package=&symbol=&os=&runtime=&runtimeVersion=",
			"Search the network from a URL: packages, symbols, error codes, graded against the environment you name. The same pipeline the MCP tool and the CLI run; a miss is spelled grade=NO_SAFE_MATCH."},
		{"POST", "/v2/search",
			"The same search as a JSON body: {schemaVersion:2, query, packages[], symbols[], environment{}}. Each result carries sampleUrl, exact[], different[] and adaptationNeeded[]."},
		{"POST", "/v1/search",
			"The frozen v1 response shape, for clients that pinned it."},
		{"GET", "/v1/registry/packages/{purl}",
			"One package's compatibility snapshot, by PURL. Percent-encode the slash: pkg:npm%2Faxios@1.12.0."},
		{"GET", "/v1/registry/symbols/{ecosystem}/{name}/{symbol}",
			"One API's snapshot: where it ran, where it failed, at what evidence level."},
		{"GET", "/v1/samples/{sampleId}",
			"A published sample's manifest: its case, its contract, its verification receipts."},
		{"GET", "/v1/samples/{sampleId}/artifact",
			"That sample's files. The id is the hash of the contents, so the bytes verify themselves."},
		{"GET", "/findings.json?eco=&os=&runtime=&q=&basis=",
			"Every finding the /findings page shows, as one document: what was believed, what was measured, and the sample that proves it."},
		{"GET", "/v1/wanted",
			"What the network has been asked for and has no sample for yet."},
		{"GET", "/v1/adapters",
			"The ecosystems and lockfiles the scanner reads, and which ecosystems verify."},
		{"GET", "/v1/stats",
			"Observation, sample and package counts — what the front page's tiles are drawn from."},
		{"GET", "/v1/builder",
			"How the aggregation builder is doing: its last successful pass and age, its last failure and why (timeout, lease_lost, canceled, error), repair progress, and whether the stats clock is more than a day old."},
		{"GET", "/v1/shards/{ecosystem}/{name}/{n}",
			"The offline shard a client syncs, so a machine can answer without asking again. ETag-cached."},
		{"GET", "/v1/peers/for-sample/{sampleId}",
			"Peers holding that sample, for fetching it without this server."},
		{"POST", "/v1/footprints/execution",
			"The one write a zero-install caller may make: {sampleId, outcome: pass|fail|could_not_run, stage, environment{os,arch,runtime,runtimeVersion}}. Recorded as an unsigned self-report that weighs nothing in any grade."},
		{"GET", "/skill.md",
			"This surface as a machine-readable guide for an agent that can fetch a URL and nothing else."},
		{"GET", "/version",
			"Which build of the server answered, and in which environment."},
	}
}
