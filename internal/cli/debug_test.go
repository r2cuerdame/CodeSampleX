package cli

import (
	"context"
	"testing"
)

func TestGlobalDebugFlagAndEnvironmentUseTheSameContext(t *testing.T) {
	enabled, rest := parseGlobalDebug([]string{"--debug", "search", "axios"})
	if !enabled || len(rest) != 2 || rest[0] != "search" {
		t.Fatalf("global debug parse = %v %v", enabled, rest)
	}
	ctx := context.WithValue(context.Background(), debugContextKey{}, true)
	if !debugEnabled(ctx) {
		t.Fatal("--debug context did not enable diagnostics")
	}
	t.Setenv("CSX_DEBUG", "1")
	if !debugEnabled(context.Background()) {
		t.Fatal("CSX_DEBUG did not enable the same diagnostic path")
	}
	t.Setenv("CSX_DEBUG", "0")
	if debugEnabled(context.Background()) {
		t.Fatal("CSX_DEBUG=0 must keep diagnostics off")
	}
}
