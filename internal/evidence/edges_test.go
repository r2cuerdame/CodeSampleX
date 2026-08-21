package evidence

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func edge(parent, child string) scanner.Edge {
	p, err := domain.ParsePURL(parent)
	if err != nil {
		panic(err)
	}
	c, err := domain.ParsePURL(child)
	if err != nil {
		panic(err)
	}
	return scanner.Edge{Parent: p, Child: c}
}

// An edge is a fact about two PUBLIC packages, which is already registry
// information. One end private makes it a fact about somebody's private code
// and it does not travel — the same rule packages themselves follow, applied
// where the edges are chosen rather than a second time somewhere else.
func TestOnlyEdgesBetweenPublicPackagesTravel(t *testing.T) {
	public := map[string]domain.PURL{}
	for _, s := range []string{"pkg:npm/a@1.2.0", "pkg:npm/b@1.9.0"} {
		p, _ := domain.ParsePURL(s)
		public[s] = p
	}
	got := publicEdges([]scanner.Edge{
		edge("pkg:npm/a@1.2.0", "pkg:npm/b@1.9.0"),
		edge("pkg:npm/a@1.2.0", "pkg:npm/secret@0.0.1"),
		edge("pkg:npm/secret@0.0.1", "pkg:npm/b@1.9.0"),
	}, public)

	want := map[string][]string{"pkg:npm/a@1.2.0": {"pkg:npm/b@1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
}

// The server refuses a batch whose edge list is bigger than any honest
// package's direct dependencies — and a refused batch loses the observation
// riding in it. The clamp lives here so a pathological parent keeps its first
// children instead of costing the whole batch.
func TestPublicEdgesClampToTheWireCap(t *testing.T) {
	public := map[string]domain.PURL{}
	parent := "pkg:npm/mega@1.0.0"
	p, _ := domain.ParsePURL(parent)
	public[parent] = p
	var edges []scanner.Edge
	for i := 0; i < domain.MaxDependsOnPerBatch+8; i++ {
		child := fmt.Sprintf("pkg:npm/dep%d@1.0.0", i)
		c, _ := domain.ParsePURL(child)
		public[child] = c
		edges = append(edges, scanner.Edge{Parent: p, Child: c})
	}
	got := publicEdges(edges, public)
	if len(got[parent]) != domain.MaxDependsOnPerBatch {
		t.Fatalf("len(children) = %d, want the wire cap %d", len(got[parent]), domain.MaxDependsOnPerBatch)
	}
}

// The same edge listed twice is one relationship seen twice.
func TestDuplicateEdgesCollapse(t *testing.T) {
	public := map[string]domain.PURL{}
	for _, s := range []string{"pkg:npm/a@1.2.0", "pkg:npm/b@1.9.0"} {
		p, _ := domain.ParsePURL(s)
		public[s] = p
	}
	got := publicEdges([]scanner.Edge{
		edge("pkg:npm/a@1.2.0", "pkg:npm/b@1.9.0"),
		edge("pkg:npm/a@1.2.0", "pkg:npm/b@1.9.0"),
	}, public)
	if want := []string{"pkg:npm/b@1.9.0"}; !reflect.DeepEqual(got["pkg:npm/a@1.2.0"], want) {
		t.Errorf("children = %v, want %v", got["pkg:npm/a@1.2.0"], want)
	}
}
