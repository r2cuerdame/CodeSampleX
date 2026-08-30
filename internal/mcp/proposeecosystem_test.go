package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A clean-room workspace must not be scaffolded for an ecosystem this network
// cannot verify.
//
// Reported as report_csx_issue 8 ("propose_public_sample rejects a supplied
// pkg:pub/... purl with the same message it returns when packages is omitted
// entirely"). The pub half is fixed — pub joined AllowedEcosystems — but
// checking it turned up the larger half: the allowlist is consulted in exactly
// ONE place, serverstore/validate.go, the server's batch ingest. Nothing on
// the propose path looks at it.
//
// Verified live against production before this test existed:
// pkg:nuget/Newtonsoft.Json@13.0.3 was accepted and a workspace scaffolded,
// though nuget is in no allowlist and imageFor("nuget") errors. An agent would
// write the whole C# sample and learn at verify time that it can never be
// published. The tool's contract promised a route that does not exist.
func TestProposeRefusesAnEcosystemTheNetworkCannotVerify(t *testing.T) {
	s := newProposeTestServer(t)

	res := s.toolPropose(t.Context(), json.RawMessage(
		`{"goal":"serialize an object","packages":["pkg:nuget/Newtonsoft.Json@13.0.3"]}`))
	if res == nil {
		t.Fatal("no result")
	}
	body := resultText(res)
	if !res.IsError {
		t.Fatalf("an unverifiable ecosystem was accepted:\n%s", body)
	}
	// The message has to name what is wrong. Report 8's complaint was that a
	// rejected purl and an omitted packages list returned the SAME text, which
	// tells an agent nothing about which mistake it made.
	if !strings.Contains(body, "nuget") {
		t.Errorf("the refusal does not name the ecosystem it refused:\n%s", body)
	}
	if strings.Contains(body, "packages is required") {
		t.Errorf("a rejected coordinate reports the same thing as an empty list:\n%s", body)
	}
}

// Every ecosystem the network does verify still goes through, including the
// verification-only ones that have no local scanner.
func TestProposeAcceptsEveryVerifiableEcosystem(t *testing.T) {
	// Checked at the gate rather than through toolPropose: past the gate the
	// call reaches Propose, which needs a real workspace root. What is under
	// test is which coordinates the gate lets through.
	for _, purl := range []string{
		"pkg:npm/axios@1.12.0",
		"pkg:pypi/requests@2.32.3",
		"pkg:cargo/serde@1.0.0",
		"pkg:golang/github.com/pkg/errors@v0.9.1",
		"pkg:maven/com.google.guava/guava@33.0.0-jre",
		"pkg:gem/rack@3.1.8",
		"pkg:hex/jason@1.4.4",
		"pkg:pub/yaml@3.1.2",
	} {
		if bad, refused := unverifiableCoordinate([]string{purl}); refused {
			t.Errorf("%s was refused as unverifiable", bad)
		}
	}
}

// newProposeTestServer builds a server whose Propose would succeed, so a
// refusal can only come from the ecosystem check under test.
func newProposeTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{Deps: &Deps{}}
}
