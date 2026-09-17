package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// saturatedGate returns a store whose four admission slots are all held,
// and the release that frees them.
func saturatedGate(t *testing.T, budget time.Duration) (*webStore, func()) {
	t.Helper()
	w := &webStore{packageLoadAdmissionBudget: budget} // no store: reaching the database would panic.
	w.packageLoadOnce.Do(func() { w.packageLoadSlots = make(chan struct{}, packageLoadSlotCount) })
	for range packageLoadSlotCount {
		w.packageLoadSlots <- struct{}{}
	}
	return w, func() {
		for range packageLoadSlotCount {
			<-w.packageLoadSlots
		}
	}
}

func interactiveRequestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, _ := withRequestPressure(
		serverstore.WithQueryBudget(t.Context(), serverstore.NewQueryBudget(serverstore.ClassInteractive)))
	return ctx
}

// The allowance belongs to the request, not to the read. Once a request
// has stood at the gate for its whole allowance, its next cold read is
// refused at once: this is what keeps five serial cold reads from costing
// five allowances (#174), now that a single one is twelve times the old
// per-read patience.
func TestAdmissionAllowanceIsSpentOncePerRequest(t *testing.T) {
	const budget = 120 * time.Millisecond
	w, release := saturatedGate(t, budget)
	defer release()
	ctx := interactiveRequestCtx(t)

	first := time.Now()
	err := w.withPackageLoadSlot(ctx, func() error { t.Error("refused read ran"); return nil })
	firstWait := time.Since(first)
	if !isAdmissionRefusal(err) {
		t.Fatalf("first read: %v, want admission refusal", err)
	}
	if firstWait < budget {
		t.Errorf("first read was refused after %v, before its %v allowance ran out", firstWait, budget)
	}

	second := time.Now()
	err = w.withPackageLoadSlot(ctx, func() error { t.Error("refused read ran"); return nil })
	secondWait := time.Since(second)
	if !isAdmissionRefusal(err) {
		t.Fatalf("second read: %v, want admission refusal", err)
	}
	if secondWait >= budget/2 {
		t.Errorf("second read waited %v with the request's allowance already spent, want an immediate refusal", secondWait)
	}
}

// Reads a page issues side by side are charged for the time the visitor
// waited, not for the sum of their waits: four reads standing at the gate
// together for one allowance must leave the request refused after one
// allowance, and a request whose parallel reads waited half of it must
// still have the other half for the read that decides the page.
func TestAdmissionAllowanceChargesParallelWaitsOnce(t *testing.T) {
	const budget = 400 * time.Millisecond
	w, release := saturatedGate(t, budget)
	ctx := interactiveRequestCtx(t)

	// Four reads wait together; free the gate at half the allowance.
	var wg sync.WaitGroup
	admitted := make(chan struct{}, packageLoadSlotCount)
	started := time.Now()
	for range packageLoadSlotCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.withPackageLoadSlot(ctx, func() error { admitted <- struct{}{}; return nil })
			if err != nil {
				t.Errorf("parallel read: %v, want admission once the gate opened", err)
			}
		}()
	}
	time.Sleep(budget / 2)
	release()
	wg.Wait()
	if got := len(admitted); got != packageLoadSlotCount {
		t.Fatalf("%d parallel reads admitted, want %d", got, packageLoadSlotCount)
	}
	clock := admissionClockOf(ctx)
	spent := clock.spentAt(time.Now())
	if spent < budget/4 || spent >= budget {
		t.Fatalf("four parallel waits of ~%v were charged as %v, want about one wait (elapsed %v)",
			budget/2, spent, time.Since(started))
	}

	// The required read that follows still has its half.
	for range packageLoadSlotCount {
		w.packageLoadSlots <- struct{}{}
	}
	go func() {
		time.Sleep(budget / 8)
		<-w.packageLoadSlots
	}()
	err := w.withPackageLoadSlot(ctx, func() error { return nil })
	if err != nil {
		t.Fatalf("the read after the parallel phase was refused (%v): the parallel waits were charged as their sum", err)
	}
}

// A background stale-cache refresh has nothing waiting on it and keeps the
// short patience: it must not hold a goroutine for a visitor's allowance
// when it already has a value to serve.
func TestBackgroundRefreshKeepsShortAdmissionPatience(t *testing.T) {
	w, release := saturatedGate(t, time.Hour)
	defer release()
	ctx := backgroundRefreshBudget(false)

	started := time.Now()
	err := w.withPackageLoadSlot(ctx, func() error { t.Error("refused read ran"); return nil })
	if !isAdmissionRefusal(err) {
		t.Fatalf("background read: %v, want admission refusal", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*packageLoadAdmissionWait {
		t.Errorf("background refresh waited %v at a saturated gate, want about %v", elapsed, packageLoadAdmissionWait)
	}
}

// An interactive read with no request behind it -- the pre-#426 shape every
// existing gate test uses -- is unchanged too.
func TestInteractiveReadWithoutRequestKeepsShortAdmissionPatience(t *testing.T) {
	w, release := saturatedGate(t, time.Hour)
	defer release()
	ctx := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)

	started := time.Now()
	err := w.withPackageLoadSlot(ctx, func() error { t.Error("refused read ran"); return nil })
	if !isAdmissionRefusal(err) {
		t.Fatalf("read: %v, want admission refusal", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*packageLoadAdmissionWait {
		t.Errorf("request-less read waited %v, want about %v", elapsed, packageLoadAdmissionWait)
	}
}
