package mcp

import (
	"context"
	"encoding/json"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"testing"
	"time"
)

func TestObservedCommandReceivesExplicitDeadlineAndRejectsInvalidBudget(t *testing.T) {
	deps := emptyDeps()
	called := false
	deps.RunObserved = func(ctx context.Context, _ []string, _ string) (int, string, string, []string, commandOutput, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("command deadline missing")
		}
		return 1, "PROJECT_PROCESS", "FAIL", nil, commandOutput{Stderr: "SDK location not found"}, nil
	}
	server := &Server{Deps: deps}
	result := server.toolRunObserved(context.Background(), json.RawMessage(`{"command":["gradlew"],"timeoutSeconds":1}`))
	if result.IsError || !called {
		t.Fatalf("command failed to return: %+v", result)
	}
	called = false
	result = server.toolRunObserved(context.Background(), json.RawMessage(`{"command":["gradlew"],"timeoutSeconds":-1}`))
	if !result.IsError || called {
		t.Fatal("invalid deadline executed command")
	}
}
func TestSupportingLookupIgnoringCancellationCannotHoldCompletedCommand(t *testing.T) {
	deps := emptyDeps()
	release := make(chan struct{})
	defer close(release)
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		<-release
		return domain.SearchResponse{Miss: true}, ""
	}
	server := &Server{Deps: deps}
	started := time.Now()
	result := server.lookupAfterFailureWithBudget(context.Background(), []string{"gradlew"}, ".", []string{"SDK location not found"}, 40*time.Millisecond)
	if result.Status != "UNAVAILABLE" || time.Since(started) > time.Second {
		t.Fatalf("supporting lookup blocked: %+v", result)
	}
}
