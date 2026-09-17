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

//go:embed skill/skill.md
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
