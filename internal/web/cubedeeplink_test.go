package web

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A shared URL keeps pointing at the coordinate it named.
//
// The cube assembly reads the newest cubeMaxVersions releases and the first
// cubeMaxSymbolsPerVersion symbols of each, which is a sensible bound for
// BROWSING. Applied to a link that already names its coordinate — ?f_version=
// and ?f_symbol= — it is a bug that arrives with time: every release pushes an
// older one out of the window, and the day it leaves, every link anyone ever
// shared for it starts answering "No recorded evidence matches these filters".
//
// The evidence never moved. Only the window did.

// deepLinkStore holds nine releases, each measured, each with one symbol —
// so the browse window covers six of them and three are only ever reachable
// by naming them.
func deepLinkStore() *fakeStore {
	f := newFakeStore()
	vers := []string{"7.7.2", "7.7.1", "7.6.0", "7.5.4", "7.5.3", "7.3.8", "7.0.0", "6.3.1", "6.3.0"}
	f.versions["npm|semverish"] = vers
	for _, v := range vers {
		purl := "pkg:npm/semverish@" + v
		f.symbols["npm|semverish|"+v] = []string{"semver.clean"}
		f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "ubuntu", "x64",
			"node", "22", "npm", "PROJECT_COMPILE", 3, 0)
		f.snapshots[snapKey(purl, "semver.clean")] = cubeSnap(purl, "semver.clean", "ubuntu", "x64",
			"node", "22", "npm", "CONTRACT", 3, 0)
	}
	return f
}

// The browse assembly is deliberately unchanged: it still stops at six.
func TestTheBrowseWindowStillBoundsAnUnpinnedAssembly(t *testing.T) {
	facts, windowed, err := loadCubeFacts(context.Background(), deepLinkStore(), "npm", "semverish")
	if err != nil {
		t.Fatal(err)
	}
	if !windowed {
		t.Error("nine versions read without reporting that the window trimmed anything")
	}
	if got := len(cubeDimValues(facts, "version")); got != cubeMaxVersions {
		t.Errorf("browse assembly covers %d versions, want the %d-version cap kept", got, cubeMaxVersions)
	}
}

// A version the reader named is loaded whether or not the browse window
// reached it.
func TestAPinnedVersionOutsideTheBrowseWindowIsLoaded(t *testing.T) {
	store := deepLinkStore()
	have, _, err := loadCubeFacts(context.Background(), store, "npm", "semverish")
	if err != nil {
		t.Fatal(err)
	}
	filters := map[string]string{"version": "6.3.1"}
	if len(filterCubeFacts(have, filters)) != 0 {
		t.Fatal("fixture no longer puts 6.3.1 outside the browse window")
	}
	extra := loadPinnedCubeFacts(context.Background(), store, "npm", "semverish", have, filters)
	if len(filterCubeFacts(extra, filters)) == 0 {
		t.Fatal("a pinned version outside the window loaded nothing: its links are dead")
	}
}

// The same for a symbol past the per-version cap.
func TestAPinnedSymbolPastTheCapIsLoaded(t *testing.T) {
	f := newFakeStore()
	const v = "1.0.0"
	purl := "pkg:npm/widish@" + v
	f.versions["npm|widish"] = []string{v}
	var syms []string
	for i := 0; i < cubeMaxSymbolsPerVersion+4; i++ {
		sym := "wid.f" + strconv.Itoa(i)
		syms = append(syms, sym)
		f.snapshots[snapKey(purl, sym)] = cubeSnap(purl, sym, "ubuntu", "x64",
			"node", "22", "npm", "CONTRACT", 1, 0)
	}
	f.symbols["npm|widish|"+v] = syms
	last := syms[len(syms)-1]

	have, windowed, err := loadCubeFacts(context.Background(), f, "npm", "widish")
	if err != nil {
		t.Fatal(err)
	}
	if !windowed {
		t.Fatal("fixture no longer trips the symbol cap")
	}
	filters := map[string]string{"symbol": last}
	if len(filterCubeFacts(have, filters)) != 0 {
		t.Fatalf("fixture no longer puts %s past the cap", last)
	}
	extra := loadPinnedCubeFacts(context.Background(), f, "npm", "widish", have, filters)
	if len(filterCubeFacts(extra, filters)) == 0 {
		t.Fatalf("a pinned symbol past the cap loaded nothing: %s is unreachable", last)
	}
}

// The trap the two single-dimension checks miss: BOTH values are inside the
// window, and the PAIR is not. The symbol is the first of one release and the
// twelfth of another, so a probe on either name alone finds evidence and
// concludes there is nothing to load.
func TestAPinnedPairInsideNeitherWindowSliceIsLoaded(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|pairish"] = []string{"2.0.0", "1.0.0"}
	const sym = "pair.late"
	for _, v := range []string{"2.0.0", "1.0.0"} {
		purl := "pkg:npm/pairish@" + v
		var syms []string
		if v == "2.0.0" {
			// The pinned symbol is past the cap on this release.
			for i := 0; i < cubeMaxSymbolsPerVersion+1; i++ {
				syms = append(syms, "pair.f"+strconv.Itoa(i))
			}
			syms = append(syms, sym)
		} else {
			syms = []string{sym}
		}
		for _, s := range syms {
			f.snapshots[snapKey(purl, s)] = cubeSnap(purl, s, "ubuntu", "x64",
				"node", "22", "npm", "CONTRACT", 2, 0)
		}
		f.symbols["npm|pairish|"+v] = syms
	}

	have, _, err := loadCubeFacts(context.Background(), f, "npm", "pairish")
	if err != nil {
		t.Fatal(err)
	}
	filters := map[string]string{"version": "2.0.0", "symbol": sym}
	if len(filterCubeFacts(have, filters)) != 0 {
		t.Fatal("fixture no longer hides the pair from the browse window")
	}
	extra := loadPinnedCubeFacts(context.Background(), f, "npm", "pairish", have, filters)
	if len(filterCubeFacts(extra, filters)) == 0 {
		t.Fatal("the pinned pair loaded nothing: a shared exact link reads as No match")
	}
}

// Nothing extra is read when the window already covered the pin. The load is
// a repair, not a second assembly on every request.
func TestAPinInsideTheWindowReadsNothingExtra(t *testing.T) {
	store := deepLinkStore()
	have, _, err := loadCubeFacts(context.Background(), store, "npm", "semverish")
	if err != nil {
		t.Fatal(err)
	}
	filters := map[string]string{"version": "7.7.2", "symbol": "semver.clean"}
	if got := loadPinnedCubeFacts(context.Background(), store, "npm", "semverish", have, filters); got != nil {
		t.Errorf("re-read %d facts the browse window already had", len(got))
	}
}

// End to end: the shared URL from the issue. The page must answer for the
// coordinate rather than reporting an absence the window invented.
func TestADeepLinkPastTheBrowseWindowIsNotAFalseNoMatch(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = deepLinkStore() })
	body := get(t, mux, "/npm/semverish?f_symbol=semver.clean&f_version=6.3.1").Body.String()

	if strings.Contains(body, "No recorded evidence matches these filters") {
		t.Fatal("a coordinate with evidence reads as No match because the browse window skipped it")
	}
	mustContain(t, body, "semver.clean")
	mustContain(t, body, "6.3.1")
}

// "No match" has to keep meaning what it says: a coordinate that really has
// no evidence still says so, and does not get a fabricated record from the
// repair load.
func TestADeepLinkToNothingStillSaysNoMatch(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = deepLinkStore() })
	body := get(t, mux, "/npm/semverish?f_version=99.9.9").Body.String()
	mustContain(t, body, "No recorded evidence matches these filters")
}

// countingStore records how many snapshots a request actually read.
type countingStore struct {
	*fakeStore
	reads int
}

func (c *countingStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	c.reads++
	return c.fakeStore.SnapshotJSON(ctx, purl, symbol)
}

// The repair read is memoized. Without it, the URLs most likely to be shared
// — an exact coordinate on an older release — would be the only ones on the
// site paying for uncached snapshot reads on every hit, against a connection
// pool of eight.
func TestTheRepairReadIsNotRepeatedOnEveryHit(t *testing.T) {
	store := &countingStore{fakeStore: deepLinkStore()}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })
	const link = "/npm/semverish?f_symbol=semver.clean&f_version=6.3.1"

	get(t, mux, link)
	first := store.reads
	if first == 0 {
		t.Fatal("the first request read nothing at all")
	}
	store.reads = 0
	body := get(t, mux, link).Body.String()
	if store.reads != 0 {
		t.Errorf("the second hit on the same shared link read %d snapshots again", store.reads)
	}
	// And still answers.
	if strings.Contains(body, "No recorded evidence matches these filters") {
		t.Error("the cached repair lost the coordinate it was caching")
	}
}

// The probe runs every time, so a pin the assembly already covers costs
// nothing — the repair is not a second assembly bolted onto every request.
func TestAPinTheAssemblyCoversCostsNoExtraRead(t *testing.T) {
	store := &countingStore{fakeStore: deepLinkStore()}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	get(t, mux, "/npm/semverish") // warm the assembly
	store.reads = 0
	get(t, mux, "/npm/semverish?f_symbol=semver.clean&f_version=7.7.2")
	if store.reads != 0 {
		t.Errorf("a pin inside the browse window still read %d snapshots", store.reads)
	}
}

// The window note explains an absence the window could have caused. With the
// exact coordinate named and loaded directly it can no longer have caused
// one, and the note would be telling the reader to doubt a record that was
// read on purpose.
func TestTheWindowNoteStopsAtAnExactlyPinnedCoordinate(t *testing.T) {
	if cubeWindowNote(true, map[string]string{"version": "6.3.1", "symbol": "semver.clean"}) {
		t.Error("an exactly pinned coordinate is read directly; the window explains nothing")
	}
	if !cubeWindowNote(true, map[string]string{"version": "6.3.1"}) {
		t.Error("a pinned version still has its symbols capped: the note belongs")
	}
	if !cubeWindowNote(true, nil) {
		t.Error("an unpinned browse slice lost its window note")
	}
}
