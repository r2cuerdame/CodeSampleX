package httpapi

import (
	"context"
	"strings"
	"sync"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// MavenJarProber answers whether a maven coordinate publishes a compiled
// artifact. *registry.Checker satisfies it.
//
// The second return says whether the answer is known. A registry that will
// not answer has not said no, and treating those the same way would starve
// the authoring queue on a bad afternoon — silently, which is how the last
// outage looked.
type MavenJarProber interface {
	MavenHasJar(ctx context.Context, p domain.PURL) (hasJar, known bool)
}

// dropUnauthorableMaven removes maven candidates that publish only a pom.
//
// A BOM or a parent POM declares versions for other modules and contains no
// classes, so there is no symbol a contract could call and the assignment can
// never finish. That is the same waste as the Gradle plugin marker, and after
// that one was excluded these were the remaining items the Wanted board could
// not cover.
//
// It asks the registry rather than reading the name. "-bom" and "-parent" are
// conventions authors choose, not a generated marker like ".gradle.plugin",
// and a false exclusion loses real authoring work permanently and silently.
func dropUnauthorableMaven(ctx context.Context, prober MavenJarProber,
	candidates []serverstore.WantedRow) []serverstore.WantedRow {
	if prober == nil || len(candidates) == 0 {
		return candidates
	}
	out := candidates[:0:0]
	for _, c := range candidates {
		if !strings.EqualFold(c.Ecosystem, "maven") {
			out = append(out, c)
			continue
		}
		hasJar, known := prober.MavenHasJar(ctx, domain.PURL{
			Ecosystem: "maven", Name: c.Name, Version: c.Version,
		})
		if known && !hasJar {
			continue
		}
		out = append(out, c)
	}
	return out
}

// cachedMavenJarProber remembers each verdict for the life of the process.
//
// An artifact does not grow a jar later, and the same handful of coordinates
// is re-evaluated on every worker poll — several times a minute, against a
// third-party registry. Only KNOWN answers are kept: an unanswered probe is
// worth retrying, and caching "we could not tell" would turn one bad minute
// into a permanent exclusion.
type cachedMavenJarProber struct {
	inner MavenJarProber
	mu    sync.Mutex
	known map[string]bool // purl -> has jar
}

func newCachedMavenJarProber(inner MavenJarProber) MavenJarProber {
	if inner == nil {
		return nil
	}
	return &cachedMavenJarProber{inner: inner, known: map[string]bool{}}
}

func (c *cachedMavenJarProber) MavenHasJar(ctx context.Context, p domain.PURL) (bool, bool) {
	key := p.String()
	c.mu.Lock()
	hasJar, ok := c.known[key]
	c.mu.Unlock()
	if ok {
		return hasJar, true
	}
	hasJar, known := c.inner.MavenHasJar(ctx, p)
	if known {
		c.mu.Lock()
		c.known[key] = hasJar
		c.mu.Unlock()
	}
	return hasJar, known
}
