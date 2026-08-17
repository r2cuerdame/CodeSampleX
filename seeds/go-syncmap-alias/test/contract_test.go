package contract_test

import (
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sync/syncmap"
)

func TestMapIsTheStandardLibraryTypeAlias(t *testing.T) {
	var legacy syncmap.Map
	var standard *sync.Map = &legacy
	var roundTrip *syncmap.Map = standard
	if roundTrip != &legacy {
		t.Fatal("syncmap.Map and sync.Map did not have pointer identity")
	}

	if _, ok := legacy.Load("missing"); ok {
		t.Fatal("the zero Map was not empty")
	}
}

func TestMapIsNotGeneric(t *testing.T) {
	cmd := exec.Command("go", "test", "-tags", "genericprobe", "./probe/generic")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("syncmap.Map[string, int] unexpectedly compiled")
	}
	message := string(out)
	if !strings.Contains(message, "syncmap.Map is not a generic type") {
		t.Fatalf("unexpected generic-instantiation error:\n%s", message)
	}
}

func TestStoreLoadOrStoreAndCompareAndSwap(t *testing.T) {
	var values syncmap.Map
	values.Store("version", 1)

	if got, ok := values.Load("version"); !ok || got != 1 {
		t.Fatalf("Load(version) = %v, %v; want 1, true", got, ok)
	}
	if actual, loaded := values.LoadOrStore("version", 99); !loaded || actual != 1 {
		t.Fatalf("existing LoadOrStore = %v, %v; want 1, true", actual, loaded)
	}
	if actual, loaded := values.LoadOrStore("new", 2); loaded || actual != 2 {
		t.Fatalf("new LoadOrStore = %v, %v; want 2, false", actual, loaded)
	}

	if !values.CompareAndSwap("version", 1, 2) {
		t.Fatal("CompareAndSwap did not replace the matching value")
	}
	if values.CompareAndSwap("version", 1, 3) {
		t.Fatal("CompareAndSwap replaced a value from a stale expected value")
	}
	if got, _ := values.Load("version"); got != 2 {
		t.Fatalf("version = %v; want 2", got)
	}
}

func TestCompareAndSwapRequiresAComparableOldValue(t *testing.T) {
	var values syncmap.Map
	values.Store("slice", []int{1})

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("CompareAndSwap with a slice old value did not panic")
		}
	}()
	values.CompareAndSwap("slice", []int{1}, []int{2})
}

func TestConcurrentLoadOrStorePublishesOneWinner(t *testing.T) {
	const workers = 64
	var values syncmap.Map
	start := make(chan struct{})
	actuals := make(chan any, workers)
	var newStores atomic.Int32
	var group sync.WaitGroup

	for candidate := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			actual, loaded := values.LoadOrStore("winner", candidate)
			if !loaded {
				newStores.Add(1)
			}
			actuals <- actual
		}()
	}
	close(start)
	group.Wait()
	close(actuals)

	winner, ok := values.Load("winner")
	if !ok {
		t.Fatal("winner key was absent")
	}
	if got := newStores.Load(); got != 1 {
		t.Fatalf("new stores = %d; want exactly 1", got)
	}
	for actual := range actuals {
		if actual != winner {
			t.Fatalf("caller observed %v; stored winner is %v", actual, winner)
		}
	}
}

func TestRangeCanStopEarlyAndClearLeavesAReusableMap(t *testing.T) {
	var values syncmap.Map
	values.Store("a", 1)
	values.Store("b", 2)
	values.Store("c", 3)

	visited := 0
	values.Range(func(_, _ any) bool {
		visited++
		return false
	})
	if visited != 1 {
		t.Fatalf("early-stopped Range visited %d entries; want 1", visited)
	}

	values.Clear()
	values.Range(func(key, value any) bool {
		t.Fatalf("Clear left %v=%v", key, value)
		return true
	})
	values.Store("after-clear", 4)
	if got, ok := values.Load("after-clear"); !ok || got != 4 {
		t.Fatalf("reused map Load = %v, %v; want 4, true", got, ok)
	}
}
