package main

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/admin"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// adminStore keeps optional operational reads out of serverstore.Store. A
// deployment backed by PG gets the richer dashboard; lightweight fakes and
// alternate stores keep working and report the capability as unavailable.
type adminStore struct {
	serverstore.Store
}

func newAdminStore(store serverstore.Store) admin.Store {
	if store == nil {
		return nil
	}
	return &adminStore{Store: store}
}

func (s *adminStore) AdminInsights(ctx context.Context, now time.Time) (serverstore.AdminInsights, bool, error) {
	reader, ok := s.Store.(serverstore.AdminInsightsReader)
	if !ok {
		return serverstore.AdminInsights{}, false, nil
	}
	insights, err := reader.AdminInsights(ctx, now)
	return insights, true, err
}
