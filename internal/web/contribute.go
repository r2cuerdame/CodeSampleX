package web

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// This file is /contribute: what the network accepts from strangers, and
// what it does not.
//
// It exists because the publish endpoint refuses anonymous uploads and
// points here. A gate that says only "forbidden" teaches nothing and reads
// as a closed project; the same gate beside a page naming three open
// channels reads as a policy. The refusal message and this page are one
// thing in two places, and they must not drift — publishgate_test.go pins
// the message, and TestContributePageAnswersTheRefusal pins that this page
// answers it.
type contributePage struct {
	basePage
	// SourceURL is the repository. Everything on this page is checkable
	// there, which is the only reason a stranger should believe it.
	SourceURL string
	IssuesURL string
	// WorkerPrompt is a separate install path for spare machines. It uses
	// --no-agents so contributing compute never mutates an MCP client config.
	WorkerPrompt string
}

func (s *site) contribute(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	b := s.page(r, lang, i18n.T(lang, "contribute.title")+" — CodeSampleX",
		i18n.T(lang, "meta.contribute"))
	s.render(w, "contribute", http.StatusOK, contributePage{
		basePage:     b,
		SourceURL:    "https://github.com/r2cuerdame/CodeSampleX",
		IssuesURL:    "https://github.com/r2cuerdame/CodeSampleX/issues/new",
		WorkerPrompt: workerPrompt(lang, s.base(r)),
	})
}

// workerPrompt renders the worker-only install path from the same first-party
// origin the visitor is reading. The visible prompt and its copy action both
// use this field, so the instructions can be inspected before they are sent.
func workerPrompt(lang, base string) string {
	return i18n.T(lang, "landing.worker_prompt", base, base)
}
