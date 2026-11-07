package serverstore

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"
)

// A cross job carries the environment its sample needs. The queue filtered on
// sandboxCapability and not on the operating system, so a Linux verifier was
// handed Windows jobs in its window — twenty rows at a time, of which the ones
// it could actually run were whatever was left. The only Windows verifier on
// the network waited behind that.
func TestOpenJobsOffersOnlyWhatTheVerifierCanRun(t *testing.T) {
	store := NewFake()
	ctx := context.Background()

	for _, s := range []struct{ id, os string }{{"sha256:linuxwork", "linux"}, {"sha256:winwork", "windows"}} {
		if err := store.SaveSample(ctx, SampleRow{
			SampleID: s.id, ManifestJSON: `{"packages":["pkg:golang/example.com/m@v1.0.0"],"symbols":[]}`,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateJob(ctx, JobRow{
			SampleID: s.id, Reason: "cross", Status: "open",
			WantEnvJSON: `{"sandboxCapability":"CONTAINER_RUN","os":"` + s.os + `"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct{ verifierOS, want string }{
		{"linux", "sha256:linuxwork"},
		{"windows", "sha256:winwork"},
	} {
		jobs, err := store.OpenJobsPage(ctx, "CONTAINER_RUN", "peer-x", "cross", tc.verifierOS, 10, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			t.Fatalf("%s verifier got %d jobs, want 1: %+v", tc.verifierOS, len(jobs), jobs)
		}
		if jobs[0].SampleID != tc.want {
			t.Errorf("%s verifier got %s, want %s", tc.verifierOS, jobs[0].SampleID, tc.want)
		}
	}

	// A job that names no OS is runnable anywhere and must not disappear.
	if err := store.SaveSample(ctx, SampleRow{
		SampleID: "sha256:anyos", ManifestJSON: `{"packages":["pkg:golang/example.com/m@v1.0.0"],"symbols":[]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, JobRow{
		SampleID: "sha256:anyos", Reason: "cross", Status: "open",
		WantEnvJSON: `{"sandboxCapability":"CONTAINER_RUN"}`,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.OpenJobsPage(ctx, "CONTAINER_RUN", "peer-x", "cross", "windows", 10, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jobs {
		if j.SampleID == "sha256:anyos" {
			found = true
		}
	}
	if !found {
		t.Errorf("an OS-agnostic job was hidden from a windows verifier: %+v", jobs)
	}
}

// The queue query's placeholders and its arguments are numbered by hand, and
// only PostgreSQL notices when they drift apart: the keyset version read $9
// and $10 from nine arguments, never read $6 or $8, and every listing came
// back 500 while the fake — which binds nothing — kept every other test green.
// This holds the numbering without a database: each $n has exactly one
// argument, and each argument is read.
func TestOpenJobsPageBindsEveryPlaceholderOnce(t *testing.T) {
	args := openJobsPageArgs("CONTAINER_RUN", "ed25519:peer", "cross", "linux", 20,
		time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), 7)
	read := map[int]bool{}
	for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(openJobsPageSQL, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if n < 1 || n > len(args) {
			t.Errorf("placeholder $%d has no argument; %d are bound", n, len(args))
		}
		read[n] = true
	}
	for n := 1; n <= len(args); n++ {
		if !read[n] {
			t.Errorf("argument $%d (%v) is bound but the query never reads it", n, args[n-1])
		}
	}
	// The zero cursor is the head of the queue, which the query reads as NULL.
	if head := openJobsPageArgs("", "", "", "", 20, time.Time{}, 0); head[6] != nil {
		t.Errorf("zero cursor bound as %v, want NULL", head[6])
	}
}

// PostgreSQL pages the queue in (created_at, id) order, and the cursor only
// means "after this row" in that order. The fake used to walk its jobs in
// insertion order instead, so a job created later with an earlier created_at
// came back after the cursor had already passed it, and the next page dropped
// it as older — a job PG would list, gone from every test the fake backs.
func TestFakeOpenJobsPageWalksCreatedAtOrderNotInsertionOrder(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	base := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	// tied-a and tied-b share a created_at, so the id half of the cursor
	// separates them: tied-a is inserted first and holds the lower id.
	for _, j := range []struct {
		sample string
		offset time.Duration
	}{
		{"sha256:third", 3 * time.Minute},
		{"sha256:first", 1 * time.Minute},
		{"sha256:tied-a", 2 * time.Minute},
		{"sha256:tied-b", 2 * time.Minute},
	} {
		if err := store.SaveSample(ctx, SampleRow{SampleID: j.sample}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateJob(ctx, JobRow{SampleID: j.sample, Reason: "cross", Status: "open",
			CreatedAt: base.Add(j.offset)}); err != nil {
			t.Fatal(err)
		}
	}
	var (
		got     []string
		after   time.Time
		afterID int64
	)
	for page := 0; page < 10; page++ {
		jobs, err := store.OpenJobsPage(ctx, "", "", "", "", 1, after, afterID)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			got = append(got, j.SampleID)
			after, afterID = j.CreatedAt, j.ID
		}
		if len(jobs) < 1 {
			break
		}
	}
	want := []string{"sha256:first", "sha256:tied-a", "sha256:tied-b", "sha256:third"}
	if !slices.Equal(got, want) {
		t.Fatalf("fake keyset walk = %v, want (created_at, id) order %v", got, want)
	}
}
