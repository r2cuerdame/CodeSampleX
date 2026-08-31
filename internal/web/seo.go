package web

import (
	"encoding/json"
	"html/template"
	"net/url"
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
//
// The SoftwareApplication here is csx, the client a visitor installs. It
// deliberately carries no softwareVersion: the only version this process
// knows is its own server build, and publishing that as the client's release
// tells every reader — and every crawler — that the CLI they are about to
// download is a commit of the website. The footer names the server build as
// the server build; see buildLineFor.
func landingJSONLD(base, lang string) []template.JS {
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

// sampleHref is the site path of a published sample page.
func sampleHref(id string) string { return "/samples/" + id }

// collectionJSONLD describes a list page and the rows currently on it.
//
// The four list pages carried no structured data at all, which left a crawler
// to infer from markup alone that /findings is a collection of findings rather
// than one long article. CollectionPage says what the page is; the ItemList
// says what is on it and in what order, which is the ordering a reader sees
// and the one the page argues for.
//
// Only the rows actually rendered are listed. A page that claimed its whole
// corpus in an ItemList would be describing something the URL does not serve,
// and the pagination is what carries the rest.
func collectionJSONLD(pageURL, name, description string, items []collectionEntry) template.JS {
	elements := make([]any, 0, len(items))
	for i, it := range items {
		if it.URL == "" || it.Name == "" {
			continue
		}
		elements = append(elements, map[string]any{
			"@type":    "ListItem",
			"position": i + 1,
			"url":      it.URL,
			"name":     it.Name,
		})
	}
	doc := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "CollectionPage",
		"url":         pageURL,
		"name":        name,
		"description": description,
	}
	if len(elements) > 0 {
		doc["mainEntity"] = map[string]any{
			"@type":           "ItemList",
			"itemListOrder":   "https://schema.org/ItemListOrderDescending",
			"numberOfItems":   len(elements),
			"itemListElement": elements,
		}
	}
	return jsonLD(doc)
}

// collectionEntry is one row of a list page, as the structured data names it.
type collectionEntry struct {
	Name string
	URL  string
}

// packageDatasetJSONLD describes one release's compatibility evidence as what
// it actually is: a dataset, machine-readable at a public URL.
//
// This is the site's own shape rather than a borrowed one. A package page is
// not an article about a library — it is the measurements this network took at
// one coordinate, and Dataset is the vocabulary for that. The distribution
// points at the registry endpoint that serves the same evidence as JSON, so
// the claim is checkable rather than decorative.
//
// No license is asserted. What may be done with this evidence has not been
// decided (#75), and a dataset that names a license it does not have is worse
// than one that names none.
//
// variableMeasured lists what the network separates and never sums: an
// observation is a build somebody ran, a verification is a contract this
// network executed in a pinned container, and conflating them is the error the
// whole evidence model exists to prevent.
func packageDatasetJSONLD(base, pageURL, ecosystem, name, version, description, lastSeen string) template.JS {
	purl := domain.PURL{Ecosystem: ecosystem, Name: name, Version: version}.String()
	doc := map[string]any{
		"@context":            "https://schema.org",
		"@type":               "Dataset",
		"url":                 pageURL,
		"name":                name + "@" + version + " (" + ecosystem + ") compatibility evidence",
		"description":         description,
		"isAccessibleForFree": true,
		"creator": map[string]any{
			"@type": "Organization",
			"name":  "CodeSampleX",
			"url":   base + "/",
		},
		"variableMeasured": []any{
			map[string]any{"@type": "PropertyValue", "name": "contract verification",
				"description": "a contract this network executed in a digest-pinned container"},
			map[string]any{"@type": "PropertyValue", "name": "project observation",
				"description": "a build or test run observed on a real developer machine"},
			map[string]any{"@type": "PropertyValue", "name": "execution environment",
				"description": "OS, architecture, libc, runtime and package manager of each run"},
		},
		"distribution": []any{
			map[string]any{
				"@type":          "DataDownload",
				"encodingFormat": "application/json",
				"contentUrl":     base + "/v1/registry/packages/" + url.PathEscape(purl),
			},
		},
	}
	if lastSeen != "" {
		doc["dateModified"] = lastSeen
	}
	return jsonLD(doc)
}

// matrixLastSeen is the newest date any row of a release's matrix carries.
//
// dateModified on the Dataset has to mean the evidence changed, not that the
// page was rendered. Rendering time is what a crawler would assume from a page
// with no date at all, and it would be wrong in the direction that matters: it
// would claim freshness this network did not measure.
func matrixLastSeen(rows []matrixRow) string {
	newest := ""
	for _, r := range rows {
		if r.LastSeen > newest {
			newest = r.LastSeen
		}
	}
	return newest
}
