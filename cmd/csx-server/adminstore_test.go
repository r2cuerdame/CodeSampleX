package main

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type insightCapableStore struct {
	serverstore.Store
	value serverstore.AdminInsights
}

func (s *insightCapableStore) AdminInsights(context.Context, time.Time) (serverstore.AdminInsights, error) {
	return s.value, nil
}

func TestAdminStoreDiscoversOptionalInsightsCapability(t *testing.T) {
	plain := newAdminStore(serverstore.NewFake())
	if _, available, err := plain.AdminInsights(t.Context(), time.Now()); err != nil || available {
		t.Fatalf("plain store insights = available:%v err:%v, want unavailable", available, err)
	}

	want := serverstore.AdminInsights{Verification: serverstore.AdminVerificationCounts{Pass: 7}}
	capable := newAdminStore(&insightCapableStore{Store: serverstore.NewFake(), value: want})
	got, available, err := capable.AdminInsights(t.Context(), time.Now())
	if err != nil || !available || got.Verification.Pass != 7 {
		t.Fatalf("capable store insights = %+v available:%v err:%v", got, available, err)
	}
}

func TestNewAdminStoreKeepsNilStoreNil(t *testing.T) {
	if got := newAdminStore(nil); got != nil {
		t.Fatalf("newAdminStore(nil) = %#v, want nil", got)
	}
}
