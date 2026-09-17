package cli

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

func TestConsentSaveWaitsForAdmittedUpdateTransaction(t *testing.T) {
	home := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	locked := make(chan error, 1)
	go func() {
		locked <- csxupdate.WithLock(home, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	cfg.AutoUpdate = "off"
	saved := make(chan error, 1)
	go func() { saved <- cfg.Save(home) }()
	select {
	case err := <-saved:
		t.Fatalf("consent save bypassed admitted update transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-locked; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
}
