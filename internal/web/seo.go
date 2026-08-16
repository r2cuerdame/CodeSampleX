package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
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

// agentNames lists the coding agents csx init configures by itself. They
// are product names, identical in every locale, and they double as the
// terms people search for ("csx claude code mcp"), so they appear in the
// page text and in the structured data rather than only in the docs.
const agentNames = "Claude Code, Codex, Gemini CLI, OpenCode"

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
			// "Does it work with <my agent>?" is the question people
			// actually type; answer it with the names, in their language.
			{
				"@type": "Question",
				"name":  i18n.T(lang, "landing.agents_heading"),
				"acceptedAnswer": map[string]any{
					"@type": "Answer",
					"text": agentNames + ". " + i18n.T(lang, "landing.agents_auto") +
						" " + i18n.T(lang, "landing.agents_models"),
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

// ecosystemLanguage maps an ecosystem to the language its packages are
// written in, where that is unambiguous. npm is deliberately absent: an
// npm sample is JavaScript or TypeScript and only the manifest's own
// environment can say which, so the field is omitted rather than guessed.
var ecosystemLanguage = map[string]string{
	"pypi": "Python", "cargo": "Rust", "golang": "Go",
}

// spdxLicenseURL is the licenses the sample pool accepts (the
// permissiveLicenses set enforced on upload in internal/httpapi), each
// with its spdx.org page. It is an explicit table rather than a pattern
// because "https://spdx.org/licenses/" + anything is a URL that may not
// exist, and a structured-data claim that 404s is a wrong claim. Every
// URL here was fetched and answers 200.
var spdxLicenseURL = map[string]string{
	"MIT-0":        "https://spdx.org/licenses/MIT-0.html",
	"MIT":          "https://spdx.org/licenses/MIT.html",
	"Apache-2.0":   "https://spdx.org/licenses/Apache-2.0.html",
	"BSD-2-Clause": "https://spdx.org/licenses/BSD-2-Clause.html",
	"BSD-3-Clause": "https://spdx.org/licenses/BSD-3-Clause.html",
	"ISC":          "https://spdx.org/licenses/ISC.html",
	"Unlicense":    "https://spdx.org/licenses/Unlicense.html",
	"CC0-1.0":      "https://spdx.org/licenses/CC0-1.0.html",
}

// sampleJSONLD describes a published sample page.
//
// A sample is a small complete project: a goal written in words, a
// contract that was executed, and the files that satisfy it. TechArticle
// carries the prose half, SoftwareSourceCode the project half. Nothing
// here asserts a verification level — that claim belongs to the receipts
// rendered on the page, which say which environment actually ran it.
func sampleJSONLD(pageURL, goal, desc, created, license string, pkgs, symbols []string, env domain.EnvironmentFingerprint) template.JS {
	code := map[string]any{
		"@type":          "SoftwareSourceCode",
		"name":           goal,
		"codeSampleType": "full (compile ready) solution",
	}
	switch lang := ecosystemLanguage[strings.ToLower(env.Ecosystem)]; {
	case env.Language != "":
		code["programmingLanguage"] = env.Language
	case lang != "":
		code["programmingLanguage"] = lang
	}
	if ctx := env.ContextLabel(); ctx != "" {
		code["runtimePlatform"] = ctx
	}

	art := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "TechArticle",
		"headline":    goal,
		"name":        goal,
		"description": desc,
		"url":         pageURL,
		// The goal and the contract lines are authored English; the page
		// chrome around them is translated, the sample text is not.
		"inLanguage": "en",
		"hasPart":    code,
		"publisher": map[string]any{
			"@type": "Organization", "name": "CodeSampleX",
		},
	}
	if created != "" {
		art["dateCreated"] = created
		art["datePublished"] = created
	}
	if u, ok := spdxLicenseURL[license]; ok {
		art["license"] = u
	}
	// keywords are the packages and symbols the sample is about — the
	// literal terms someone searches for, not invented tags. The "pkg:"
	// scheme prefix is an internal identifier and is dropped.
	kw := make([]string, 0, len(pkgs)+len(symbols))
	for _, p := range pkgs {
		kw = append(kw, strings.TrimPrefix(p, "pkg:"))
	}
	kw = append(kw, symbols...)
	if len(kw) > 0 {
		art["keywords"] = strings.Join(kw, ", ")
	}
	about := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		parsed, err := domain.ParsePURL(p)
		if err != nil {
			continue
		}
		about = append(about, map[string]any{
			"@type": "SoftwareApplication", "name": parsed.Name,
			"softwareVersion": parsed.Version, "applicationCategory": "DeveloperApplication",
		})
	}
	if len(about) > 0 {
		art["about"] = about
	}
	return jsonLD(art)
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

// sitemapSampleLimit caps the sample section of the map. The sitemap
// protocol allows 50,000 URLs per file; this leaves room for the package
// section and keeps a single /sitemap.xml request bounded.
const sitemapSampleLimit = 5000

// sampleIDRe guards the sample ids that go into the sitemap. Ids are
// content addresses ("sha256:<hex>"), and the colon is a legal path
// character that every canonical URL on the site already carries — so the
// id is emitted verbatim rather than percent-escaped, which would
// advertise a URL that differs from the page's own rel=canonical. Anything
// that is not that shape is skipped instead of guessed at.
var sampleIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]{0,127}$`)

// sampleHref is the site path of a published sample page.
func sampleHref(id string) string { return "/samples/" + id }

// sitemap lists the per-locale landing cluster (with xhtml alternates),
// the static indexable pages, the hot packages (plan P6.3) and every
// published sample.
//
// The samples are the point: each one is a page answering one specific
// question ("deny_unknown_fields with flatten", "NewFromFloat 0.1"), and
// before they were listed here nothing on the site linked to them at all —
// the seeder page is the only other route to a sample page and every
// published sample is anonymous, so all of them were unreachable.
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
	writeDated := func(loc, lastmod string) {
		b.WriteString("  <url>\n    <loc>" + xmlEscape(loc) + "</loc>\n")
		if lastmod != "" {
			b.WriteString("    <lastmod>" + xmlEscape(lastmod) + "</lastmod>\n")
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

	// /stats and /adapters are permanent redirects and stay out of the map.
	for _, p := range []string{"/records", "/findings", "/wanted", "/contribute"} {
		writeURL(base+p, nil)
	}

	if hot, err := s.d.Store.HotPackages(r.Context(), 100); err == nil {
		for _, h := range hot {
			loc := base + "/" + url.PathEscape(h.Ecosystem) + "/" + escapePathSegments(h.Name)
			writeURL(loc, nil)
		}
	}

	// Every published sample. lastmod is the publication date: a sample is
	// immutable once published (its id is the hash of its contents), so the
	// date it was created is the only honest value.
	if samples, err := s.d.Store.ListSamples(r.Context(), sitemapSampleLimit); err == nil {
		for _, sm := range samples {
			if !sampleIDRe.MatchString(sm.SampleID) {
				continue
			}
			writeDated(base+sampleHref(sm.SampleID), datePart(sm.CreatedAt))
		}
	}

	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
