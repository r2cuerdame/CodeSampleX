package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lmittmann/tint"

	"codesamplex.dev/sample/goslog/src"
)

func main() {
	// ---------------------------------------------------------------- keys
	// slog's built-in key constants, which is what a ReplaceAttr should switch
	// on instead of the string literals. The first three are the keys every
	// JSON record starts with, in this order.
	check(slog.TimeKey == "time", "TimeKey: %q", slog.TimeKey)
	check(slog.LevelKey == "level", "LevelKey: %q", slog.LevelKey)
	check(slog.MessageKey == "msg", "MessageKey: %q", slog.MessageKey)
	check(slog.SourceKey == "source", "SourceKey: %q", slog.SourceKey)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("hello")
	line := logging.Lines(&buf)[0]
	check(strings.HasPrefix(line, `{"time":"`), "line: %s", line)
	check(strings.HasSuffix(line, `","level":"INFO","msg":"hello"}`), "line: %s", line)

	// Measured, and it refutes the rule everyone repeats. "slog logs time with
	// millisecond precision" is true of TextHandler only. JSONHandler formats
	// with time.RFC3339Nano, so the fraction is variable length: trailing
	// zeros are trimmed and a whole second gets no fraction at all. A regexp
	// or a consumer that expects exactly three digits fails on the JSON
	// handler, intermittently, on whichever record happens to land on a
	// round millisecond.
	rec := decodeOne(&buf)
	ts, _ := rec["time"].(string)
	rfc3339 := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)
	check(rfc3339.MatchString(ts), "time is not RFC3339: %q", ts)

	// The same two instants through both stdlib handlers, which is the
	// difference stated without a clock in it.
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 1234567, time.UTC)
	whole := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var jsonBuf, textBuf bytes.Buffer
	check(logging.Emit(slog.NewJSONHandler(&jsonBuf, nil), slog.LevelInfo, "t", "at", fixed, "whole", whole) == nil, "emit failed")
	check(logging.Emit(slog.NewTextHandler(&textBuf, nil), slog.LevelInfo, "t", "at", fixed, "whole", whole) == nil, "emit failed")
	check(jsonBuf.String() == `{"level":"INFO","msg":"t","at":"2026-01-02T03:04:05.001234567Z","whole":"2026-01-02T03:04:05Z"}`+"\n",
		"json time: %s", jsonBuf.String())
	check(textBuf.String() == `level=INFO msg=t at=2026-01-02T03:04:05.001Z whole=2026-01-02T03:04:05.000Z`+"\n",
		"text time: %s", textBuf.String())

	// A zero Record.Time means "no timestamp", and the handler drops the
	// field rather than writing the year 1. A custom handler that formats
	// r.Time unconditionally breaks this.
	buf.Reset()
	check(logging.Emit(slog.NewJSONHandler(&buf, nil), slog.LevelWarn, "no clock") == nil, "emit failed")
	check(buf.String() == `{"level":"WARN","msg":"no clock"}`+"\n", "zero time: %s", buf.String())

	// ---------------------------------------------------------- ReplaceAttr
	// Rename, rewrite and drop, all through the one hook.
	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: logging.GCPStyle}))
	logger.Warn("disk full", "free_mb", 12)
	rec = decodeOne(&buf)
	_, hasTime := rec["time"]
	check(!hasTime, "a zero Attr must drop the field: %v", rec)
	check(rec["severity"] == "warn", "severity: %v", rec["severity"])
	check(rec["message"] == "disk full", "message: %v", rec["message"])
	check(rec["free_mb"] == float64(12), "free_mb: %v", rec["free_mb"])
	check(len(rec) == 3, "unexpected fields: %v", rec)

	// Why GCPStyle guards on len(groups). Without the guard the same switch
	// runs at every depth, so a user attribute called "msg" three levels down
	// is renamed as if it were the built-in message.
	buf.Reset()
	unguarded := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.MessageKey {
			a.Key = "message"
		}
		return a
	}
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: unguarded}))
	logger.Info("hello", slog.Group("evt", slog.String("msg", "inner")))
	check(strings.Contains(logging.Lines(&buf)[0], `"evt":{"message":"inner"}`),
		"an unguarded replacer reaches into groups: %s", buf.String())

	// Measured, and it is the limit of the guard: a top-level attribute named
	// "msg" is indistinguishable from the built-in one, because both arrive
	// with no open groups. GCPStyle renames both, JSONHandler happily writes
	// the duplicate key, and encoding/json keeps the LAST one — so the field
	// that survives is the user's, and the real message is gone.
	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: logging.GCPStyle}))
	logger.Info("hello", "msg", "user value", slog.Group("evt", slog.String("msg", "inner")))
	line = logging.Lines(&buf)[0]
	check(strings.Count(line, `"message":`) == 2, "expected a duplicate key: %s", line)
	check(strings.Contains(line, `"evt":{"msg":"inner"}`), "the guard must spare group contents: %s", line)
	rec = decodeOne(&buf)
	check(rec["message"] == "user value", "last duplicate wins: %v", rec["message"])

	// The drop rule is a ZERO Attr, not an empty key. Both halves of that are
	// asserted here because the rule is usually repeated as "return an empty
	// key", which does not drop anything.
	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: replaceTime(slog.String(slog.TimeKey, ""))}))
	logger.Info("kept")
	check(strings.HasPrefix(logging.Lines(&buf)[0], `{"time":"","level":"INFO"`),
		"an empty value keeps the field: %s", buf.String())

	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: replaceTime(slog.String("", "kept anyway"))}))
	logger.Info("kept")
	check(strings.HasPrefix(logging.Lines(&buf)[0], `{"":"kept anyway","level":"INFO"`),
		"an empty key keeps the field: %s", buf.String())

	// The call sequence itself is the contract. ReplaceAttr is called for the
	// built-ins with no open groups, and for the CONTENTS of a group with the
	// group path as its first argument — the group attribute "g" is never
	// passed. Neither is "session": it resolves to a group, and resolution
	// happens first, so ReplaceAttr only ever sees the resolved members.
	buf.Reset()
	var calls []logging.ReplaceCall
	logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: logging.Recorder(&calls)}))
	session := logging.Session{ID: "s-1", Token: "hunter2", Resolves: new(atomic.Int64)}
	logger.Info("m",
		slog.Int("a", 1),
		slog.Group("g", slog.Int("b", 2)),
		slog.Int("c", 3),
		slog.Any("session", session),
	)
	var seen []string
	for _, c := range calls {
		seen = append(seen, c.Groups+"/"+c.Key)
		check(c.Kind != slog.KindLogValuer,
			"ReplaceAttr must receive resolved values, got KindLogValuer for %q", c.Key)
		check(c.Key != "g" && c.Key != "session",
			"ReplaceAttr must not be called for a group attr: %v", c)
	}
	want := []string{"/time", "/level", "/msg", "/a", "g/b", "/c", "session/id"}
	check(strings.Join(seen, " ") == strings.Join(want, " "), "ReplaceAttr calls: %v", seen)

	// The level arrives as a KindAny holding a slog.Level, which is why the
	// replacer has to type-assert instead of reading a string.
	check(calls[1].Kind == slog.KindAny, "level kind: %v", calls[1].Kind)

	// --------------------------------------------------------------- groups
	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, nil))
	logger.WithGroup("req").With("id", "r-1").WithGroup("db").Info("query", "rows", 3)
	logger.WithGroup("empty").Info("nothing")
	logger.Info("inline", slog.Group("also_empty"))
	logger.WithGroup("").With("flat", true).Info("no group")
	out := logging.Lines(&buf)
	check(len(out) == 4, "expected 4 records, got %d", len(out))

	// WithGroup nests everything that comes after it, including a later
	// WithGroup, and the attrs attached in between land in the group that was
	// open at the time.
	check(strings.Contains(out[0], `"req":{"id":"r-1","db":{"rows":3}}`), "nesting: %s", out[0])

	// A group with nothing in it is omitted entirely — the key never appears.
	// This holds for an open group with no following attrs and for an inline
	// empty slog.Group.
	check(!strings.Contains(out[1], "empty"), "empty group must be omitted: %s", out[1])
	check(!strings.Contains(out[2], "also_empty"), "empty group must be omitted: %s", out[2])

	// WithGroup("") is a no-op, not a group with an empty name.
	check(strings.Contains(out[3], `"msg":"no group","flat":true`), "empty group name: %s", out[3])

	// ------------------------------------------------- Stringer vs LogValuer
	buf.Reset()
	logger = slog.New(slog.NewJSONHandler(&buf, nil))
	price := logging.Money{Cents: 1234, Currency: "USD"}
	check(price.String() == "12.34 USD", "String(): %q", price.String())
	logger.Info("charged", "amount", price)
	line = logging.Lines(&buf)[0]

	// The surprise: implementing fmt.Stringer buys nothing. JSONHandler hands
	// the struct to encoding/json, so the log gets the Go field names and the
	// carefully written String method is never called.
	check(strings.Contains(line, `"amount":{"Cents":1234,"Currency":"USD"}`), "amount: %s", line)
	check(!strings.Contains(line, "12.34 USD"), "String() must not be used: %s", line)

	// LogValuer is the interface that works. It also keeps the secret out of
	// the record, because the handler never sees the unresolved struct.
	buf.Reset()
	resolves := new(atomic.Int64)
	session = logging.Session{ID: "s-1", Token: "hunter2", Resolves: resolves}
	logger.Info("auth", "session", session)
	line = logging.Lines(&buf)[0]
	check(strings.Contains(line, `"session":{"id":"s-1"}`), "session: %s", line)
	check(!strings.Contains(line, "hunter2"), "token leaked: %s", line)
	check(!strings.Contains(line, "session s-1"), "String() must lose to LogValue(): %s", line)
	check(resolves.Load() == 1, "LogValue calls: %d", resolves.Load())

	// ------------------------------------------------- Enabled short-circuit
	buf.Reset()
	stats := &logging.HandlerStats{}
	logger = slog.New(logging.NewCountingHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}), stats))
	resolves = new(atomic.Int64)
	session = logging.Session{ID: "s-9", Token: "hunter2", Resolves: resolves}
	argEvals := 0
	expensive := func() string { argEvals++; return "computed" }

	logger.Debug("dropped", "session", session, "cost", expensive())
	check(stats.Enabled.Load() == 1, "Enabled calls: %d", stats.Enabled.Load())
	check(stats.Handled.Load() == 0, "Handle must be skipped when Enabled says no")
	check(buf.Len() == 0, "nothing should be written: %s", buf.String())

	// Lazy: LogValue does not run for a record that is never handled, so an
	// expensive or sensitive value costs nothing below the level.
	check(resolves.Load() == 0, "LogValue must not run for a disabled level, ran %d times", resolves.Load())

	// Measured, and it corrects the usual claim: Enabled does NOT save you
	// from evaluating arguments. Go evaluates expensive() before Debug is
	// even entered. Enabled only skips Handle and the formatting after it.
	// The two ways to actually defer work are a LogValuer, as above, or an
	// explicit logger.Enabled guard around the call.
	check(argEvals == 1, "plain arguments are evaluated regardless: %d", argEvals)

	logger.Info("kept", "session", session, "cost", expensive())
	check(stats.Handled.Load() == 1, "Handle calls: %d", stats.Handled.Load())
	check(resolves.Load() == 1, "LogValue calls: %d", resolves.Load())
	check(argEvals == 2, "arg evals: %d", argEvals)

	// The guard idiom, which is the only thing that skips the argument.
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		logger.Debug("never", "cost", expensive())
	}
	check(argEvals == 2, "guarded call must not evaluate its arguments: %d", argEvals)

	// -------------------------------------------- With resolves once, early
	// JSONHandler resolves a LogValuer attached with With at the moment With is
	// called, because WithAttrs pre-formats the attributes into the handler. It
	// is not re-resolved per record, so a value that is supposed to change over
	// time freezes at whatever it was when the child logger was built.
	buf.Reset()
	resolves = new(atomic.Int64)
	session = logging.Session{ID: "s-3", Token: "hunter2", Resolves: resolves}
	child := slog.New(slog.NewJSONHandler(&buf, nil)).With("session", session)
	check(resolves.Load() == 1, "With should resolve immediately, got %d", resolves.Load())
	child.Info("one")
	child.Info("two")
	check(resolves.Load() == 1, "With must not re-resolve per record, got %d", resolves.Load())
	for _, l := range logging.Lines(&buf) {
		check(strings.Contains(l, `"session":{"id":"s-3"}`), "line: %s", l)
	}

	// ------------------------------------------------------ the clone rule
	// WithAttrs must return a new handler. A handler that appends to its own
	// receiver instead makes the parent start logging branch=a and lets the two
	// children see each other's attributes, which is what these three lines
	// would catch.
	buf.Reset()
	stats = &logging.HandlerStats{}
	logger = slog.New(logging.NewCountingHandler(slog.NewJSONHandler(&buf, nil), stats))
	branchA := logger.With("branch", "a")
	branchB := logger.With("branch", "b")
	logger.Info("parent")
	branchA.Info("child")
	branchB.Info("child")
	out = logging.Lines(&buf)
	check(stats.WithAttrs.Load() == 2, "WithAttrs calls: %d", stats.WithAttrs.Load())
	check(!strings.Contains(out[0], "branch"), "parent was mutated: %s", out[0])
	check(strings.Contains(out[1], `"branch":"a"`), "branch a: %s", out[1])
	check(strings.Contains(out[2], `"branch":"b"`), "branch b: %s", out[2])
	check(!strings.Contains(out[2], `"a"`), "siblings share state: %s", out[2])

	// WithGroup carries the same obligation, and a receiver-mutating handler
	// fails it more visibly: the group would stay open on the parent and
	// swallow whatever the next logger writes.
	buf.Reset()
	stats = &logging.HandlerStats{}
	logger = slog.New(logging.NewCountingHandler(slog.NewJSONHandler(&buf, nil), stats))
	groupA := logger.WithGroup("ga").With("in", 1)
	groupB := logger.WithGroup("gb")
	logger.Info("parent", "k", 0)
	groupA.Info("a")
	groupB.Info("b", "k", 3)
	out = logging.Lines(&buf)
	check(stats.WithGroup.Load() == 2, "WithGroup calls: %d", stats.WithGroup.Load())
	check(strings.HasSuffix(out[0], `"msg":"parent","k":0}`), "parent must stay ungrouped: %s", out[0])
	check(strings.HasSuffix(out[1], `"ga":{"in":1}}`), "group a: %s", out[1])
	check(strings.HasSuffix(out[2], `"gb":{"k":3}}`) && !strings.Contains(out[2], "ga"),
		"siblings share an open group: %s", out[2])

	// ----------------------------------------------------------------- tint
	// tint is a third-party slog.Handler, so all of the above is supposed to
	// keep working when it replaces JSONHandler. The contract holds; what
	// changes is everything the contract does not cover — how a Stringer is
	// rendered, how much of a timestamp is printed, whether key renames are
	// visible at all, and colour.
	buf.Reset()
	resolves = new(atomic.Int64)
	session = logging.Session{ID: "s-1", Token: "hunter2", Resolves: resolves}
	plain := tint.NewTextHandler(&buf, &tint.Options{NoColor: true, Level: slog.LevelInfo})
	check(logging.Emit(plain, slog.LevelWarn, "disk full",
		"amount", price, "session", session, "free_mb", 12) == nil, "tint emit failed")

	// Same zero-time rule and same LogValuer resolution; the group LogValue
	// returns becomes a dotted key here instead of a nested object. The
	// difference that matters: tint formats a KindAny with %+v, so it DOES
	// print Money via String() where JSONHandler printed the struct fields. A
	// Stringer is handler-dependent output; only LogValuer is portable.
	check(buf.String() == `WRN disk full amount="12.34 USD" session.id=s-1 free_mb=12`+"\n",
		"tint line: %q", buf.String())

	check(!strings.Contains(buf.String(), "hunter2"), "token leaked: %s", buf.String())
	check(resolves.Load() == 1, "tint LogValue calls: %d", resolves.Load())

	// tint carries the same appendRFC3339Millis helper TextHandler uses, so a
	// time-valued attribute comes out with exactly three digits here and with
	// nanoseconds under JSONHandler. The precision of a logged timestamp is a
	// property of the handler, not of the time.Time.
	var when bytes.Buffer
	check(logging.Emit(tint.NewTextHandler(&when, &tint.Options{NoColor: true}), slog.LevelInfo, "when", "at", fixed) == nil, "tint emit failed")
	check(when.String() == "INF when at=2026-01-02T03:04:05.001Z\n", "tint time attr: %q", when.String())

	// The same ReplaceAttr written for the JSON handler works here, but only
	// its VALUES survive: tint prints time, level and message positionally,
	// so renaming msg to "message" changes nothing, while returning a zero
	// Attr for the time still drops it and rewriting the level still shows.
	buf.Reset()
	tinted := slog.New(tint.NewTextHandler(&buf, &tint.Options{NoColor: true, ReplaceAttr: logging.GCPStyle}))
	tinted.Warn("disk full", "free_mb", 12)
	check(buf.String() == "warn disk full free_mb=12\n", "tint ReplaceAttr: %q", buf.String())

	// Colors are on by default and tint does not check whether the writer is
	// a terminal, so logging to a file or a buffer gets ANSI escapes unless
	// NoColor is set. This is the reason redirected tint logs look like junk.
	buf.Reset()
	check(logging.Emit(tint.NewTextHandler(&buf, nil), slog.LevelError, "boom") == nil, "tint emit failed")
	check(strings.Contains(buf.String(), "\x1b["), "expected ANSI escapes by default: %q", buf.String())

	// v1.2.0 renamed the constructor: NewHandler is deprecated in favour of
	// NewTextHandler. Every snippet written before the rename still compiles
	// and produces identical output, which is why the deprecation is easy to
	// miss.
	var legacy bytes.Buffer
	buf.Reset()
	check(logging.Emit(tint.NewTextHandler(&buf, &tint.Options{NoColor: true}), slog.LevelInfo, "same", "k", 1) == nil, "emit failed")
	check(logging.Emit(tint.NewHandler(&legacy, &tint.Options{NoColor: true}), slog.LevelInfo, "same", "k", 1) == nil, "emit failed")
	check(buf.String() == legacy.String() && buf.String() == "INF same k=1\n", "tint constructors: %q vs %q", buf.String(), legacy.String())

	fmt.Println("contract ok")
}

// replaceTime returns a ReplaceAttr that swaps the built-in time attribute for
// with, so the "what actually drops a field" cases differ only in that value.
func replaceTime(with slog.Attr) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return with
		}
		return a
	}
}

func decodeOne(buf *bytes.Buffer) map[string]any {
	recs, err := logging.DecodeLines(buf)
	check(err == nil, "decode: %v", err)
	check(len(recs) == 1, "expected 1 record, got %d", len(recs))
	return recs[0]
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
