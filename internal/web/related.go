package web

import (
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// relatedLink is one collection reached from another, with the sentence that
// says why a reader would follow it.
type relatedLink struct {
	Href  string
	Label string
	Why   string
}

// relatedCollections is the way out of one collection into the next.
//
// Measured on the live site: of the twenty ordered pairs among Findings,
// Samples, Compatibility, Gaps and the dependency atlas, exactly ONE had a
// link in the page body -- findings to samples. Everything else was reachable
// only through the header, and two of the five are deliberately not in the
// header at all, so a reader who landed on /gaps from a search result had no
// route to any of the others.
//
// Not a row of every link on every page. Each collection points at the two
// that answer the question a reader arrives at its bottom holding, and says
// what they answer.
func relatedCollections(lang, path string) []relatedLink {
	link := func(href, label, why string) relatedLink {
		return relatedLink{Href: href, Label: i18n.T(lang, label), Why: i18n.T(lang, why)}
	}
	switch strings.TrimSuffix(path, "/") {
	case "/findings":
		return []relatedLink{
			link("/samples", "nav.samples", "related.findings_samples"),
			link("/gaps", "nav.gaps", "related.findings_gaps"),
		}
	case "/samples":
		return []relatedLink{
			link("/findings", "nav.findings", "related.samples_findings"),
			link("/gaps", "nav.gaps", "related.samples_gaps"),
		}
	case "/compatibility":
		return []relatedLink{
			link("/findings", "nav.findings", "related.compat_findings"),
			link("/gaps", "nav.gaps", "related.compat_gaps"),
		}
	case "/gaps":
		return []relatedLink{
			link("/compatibility", "nav.compatibility", "related.gaps_compat"),
			link("/samples", "nav.samples", "related.gaps_samples"),
		}
	case "/dependencies":
		return []relatedLink{
			link("/compatibility", "nav.compatibility", "related.deps_compat"),
			link("/gaps", "nav.gaps", "related.deps_gaps"),
		}
	}
	return nil
}
