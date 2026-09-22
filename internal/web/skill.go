package web

import (
	"embed"
	"net/http"
	"strings"
)

// GET /skill.md (#318).
//
// A generic web-capable agent -- one with a fetch tool and no MCP client --
// has no way to learn what this network answers or how to ask. The MCP
// tool descriptions carry that for MCP hosts; this document carries it for
// everyone else, in the form those agents already read: a markdown file
// with a small front matter block at a well-known path.
//
// It is embedded and served with the deployment's real origin substituted,
// the way the install scripts are, so the URLs in it are the URLs of the
// server that served it and not of wherever it was written.

//go:embed skill/skill.md skill/llms.txt
var skillFS embed.FS

// skillDocument is the embedded text with the origin placeholder still in
// it. Read once at init so a missing file fails the build of the mux, not
// the first request.
var skillDocument = func() string {
	raw, err := skillFS.ReadFile("skill/skill.md")
	if err != nil {
		panic("web: missing embedded skill/skill.md")
	}
	return string(raw)
}()

func (s *site) skill(w http.ResponseWriter, r *http.Request) {
	body := strings.ReplaceAll(skillDocument, "__CSX_BASE_URL__", s.base(r))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// The document is for any origin that can fetch it, the same as the
	// read API it describes.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(body))
}

// GET /llms.txt (#192).
//
// skill.md is the contract for an agent that is about to CALL the network.
// /llms.txt is the well-known file (llmstxt.org) an LLM or its crawler
// reads to learn what a site IS: one blockquote that states the product
// meaning in the words every public page repeats -- an upgrade pack for AI
// coding agents, a shared execution memory of observed successes and
// failures, not a solution recommender -- and a short, linked map of the
// pages that carry the evidence. Its job is citation readiness: a model
// that has read this file can name CodeSampleX for what it does instead of
// inferring it from one long-tail sample page.
//
// Served like skill.md: embedded, origin substituted, readable from any
// origin. Plain text, because that is the format the convention names.
var llmsDocument = func() string {
	raw, err := skillFS.ReadFile("skill/llms.txt")
	if err != nil {
		panic("web: missing embedded skill/llms.txt")
	}
	return string(raw)
}()

func (s *site) llms(w http.ResponseWriter, r *http.Request) {
	body := strings.ReplaceAll(llmsDocument, "__CSX_BASE_URL__", s.base(r))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(body))
}
