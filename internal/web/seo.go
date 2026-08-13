package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// jsonLD marshals a schema.org document for a <script type="application/ld+json">
// block. json.Marshal escapes <, > and & so the output is safe inline.
func jsonLD(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(b)
}

// landingJSONLD emits WebSite + SoftwareApplication (plan P6.3).
func landingJSONLD(base, version, lang string) []template.JS {
	website := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "WebSite",
		"name":        "CodeSampleX",
		"url":         base + "/",
		"description": i18n.T(lang, "site.meta_description"),
	}
	app := map[string]any{
		"@context":            "https://schema.org",
		"@type":               "SoftwareApplication",
		"name":                "csx",
		"operatingSystem":     "Windows, macOS, Linux",
		"applicationCategory": "DeveloperApplication",
		"downloadUrl":         base + "/install.sh",
		"offers": map[string]any{
			"@type": "Offer", "price": "0", "priceCurrency": "USD",
		},
	}
	if version != "" {
		app["softwareVersion"] = version
	}
	// The landing page answers "what is this" in prose; FAQPage lets search
	// engines surface that answer directly instead of the install command.
	faq := map[string]any{
		"@context": "https://schema.org",
		"@type":    "FAQPage",
		"mainEntity": []map[string]any{
			{
				"@type": "Question",
				"name":  i18n.T(lang, "landing.what_heading"),
				"acceptedAnswer": map[string]any{
					"@type": "Answer", "text": i18n.T(lang, "landing.what_body"),
				},
			},
			{
				"@type": "Question",
				"name":  i18n.T(lang, "landing.what_q"),
				"acceptedAnswer": map[string]any{
					"@type": "Answer", "text": i18n.T(lang, "landing.what_a"),
				},
			},
		},
	}
	org := map[string]any{
		"@context": "https://schema.org",
		"@type":    "Organization",
		"name":     "CodeSampleX",
		"url":      base + "/",
		"sameAs":   []string{repoURL, sponsorURL},
	}
	return []template.JS{jsonLD(website), jsonLD(app), jsonLD(faq), jsonLD(org)}
}

// breadcrumbJSONLD emits a BreadcrumbList for explorer pages. crumbs are
// (name, absolute URL) pairs in order.
func breadcrumbJSONLD(crumbs [][2]string) template.JS {
	items := make([]map[string]any, 0, len(crumbs))
	for i, c := range crumbs {
		items = append(items, map[string]any{
			"@type":    "ListItem",
			"position": i + 1,
			"name":     c[0],
			"item":     c[1],
		})
	}
	return jsonLD(map[string]any{
		"@context":        "https://schema.org",
		"@type":           "BreadcrumbList",
		"itemListElement": items,
	})
}

// escapePathSegments path-escapes each slash-separated segment, keeping
// the slashes (golang module paths, npm scopes).
func escapePathSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// sitemap lists the per-locale landing cluster (with xhtml alternates),
// the static indexable pages, and the hot packages (plan P6.3).
func (s *site) sitemap(w http.ResponseWriter, r *http.Request) {
	base := s.base(r)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")

	writeURL := func(loc string, alts []alternate) {
		b.WriteString("  <url>\n    <loc>" + xmlEscape(loc) + "</loc>\n")
		for _, a := range alts {
			b.WriteString(`    <xhtml:link rel="alternate" hreflang="` + a.Lang + `" href="` + xmlEscape(a.URL) + `"/>` + "\n")
		}
		b.WriteString("  </url>\n")
	}

	// Landing: one entry per locale, each carrying the full alternate cluster.
	landingAlts := landingAlternates(base)
	for _, code := range i18n.Supported {
		loc := base + "/"
		if code != i18n.Default {
			loc = base + "/" + code + "/"
		}
		writeURL(loc, landingAlts)
	}

	for _, p := range []string{"/explore", "/stats", "/adapters"} {
		writeURL(base+p, nil)
	}

	if hot, err := s.d.Store.HotPackages(r.Context(), 100); err == nil {
		for _, h := range hot {
			loc := base + "/" + url.PathEscape(h.Ecosystem) + "/" + escapePathSegments(h.Name)
			writeURL(loc, nil)
		}
	}

	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
