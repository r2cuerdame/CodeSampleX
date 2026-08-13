package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	sample "codesamplex.dev/sample/goerrgroup/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	ok := func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	tasks := []func(context.Context) error{ok, ok, ok, ok, ok, ok}

	peak, err, _ := sample.RunLimited(context.Background(), 2, tasks)
	if err != nil {
		fail("unexpected error: %v", err)
	}
	if peak > 2 {
		fail("peak concurrency = %d, want at most the limit of 2", peak)
	}

	boom := errors.New("task failed")
	failing := []func(context.Context) error{
		ok,
		func(context.Context) error { return boom },
		ok,
	}
	_, err, gctx := sample.RunLimited(context.Background(), 2, failing)
	if !errors.Is(err, boom) {
		fail("Wait returned %v, want the first task error", err)
	}
	// WithContext cancels the group context as soon as a task fails, which
	// is how the remaining tasks learn to stop.
	if gctx.Err() == nil {
		fail("group context should be canceled after a failure")
	}
	fmt.Println("CONTRACT PASS: errgroup held the limit and surfaced the first error")
}
