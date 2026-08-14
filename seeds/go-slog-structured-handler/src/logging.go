// Package logging pins down the parts of the log/slog handler contract that
// people get wrong when they write a custom handler or drop in a third-party
// one such as lmittmann/tint.
//
// slog itself is standard library, so the interesting question is not "how do
// I log a string". It is what the Handler interface actually promises, because
// every handler — yours, tint's, the JSON one — has to implement the same five
// clauses:
//
//   - Enabled is consulted first, and Handle is skipped entirely when it says
//     no. That is a promise about work not done, not about arguments: Go has
//     already evaluated everything at the call site by then.
//   - Values are resolved (slog.LogValuer) before ReplaceAttr sees them, which
//     is what makes lazy and redacted values work.
//   - ReplaceAttr is called for the built-in time/level/msg attributes with no
//     open groups, and for the contents of a group with the open groups as its
//     first argument — never for the group attribute itself.
//   - A zero Attr returned from ReplaceAttr drops the field, and a zero
//     Record.Time drops the timestamp.
//   - WithAttrs and WithGroup must return a NEW handler. Mutating the receiver
//     leaks a child logger's attributes and open groups into its parent and its
//     siblings, which is the failure the clone assertions reproduce.
//
// fmt.Stringer is not on that list, and that is the surprise this sample is
// built around: slog looks for LogValuer, never for Stringer. What a Stringer
// prints as depends entirely on the handler, so it is not something you can
// rely on.
//
// Three measurements here contradict the way this is usually described.
// Enabled saves the formatting, not the arguments. The JSON timestamp is not
// millisecond precision — that is TextHandler; JSONHandler formats with
// time.RFC3339Nano, so the fraction varies in length and vanishes on a whole
// second. And what drops a field is a zero Attr, not an empty key: an Attr with
// an empty key but a value is written out with "" as its key.
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// Money implements fmt.Stringer and nothing else.
//
// slog has no interest in Stringer. slog.Any wraps the struct as a KindAny
// value and each handler decides what that means: JSONHandler hands it to
// encoding/json, so the log gets the exported fields, while tint formats it
// with %+v and therefore does call String(). Same value, two different logs.
type Money struct {
	Cents    int64
	Currency string
}

func (m Money) String() string {
	return fmt.Sprintf("%d.%02d %s", m.Cents/100, m.Cents%100, m.Currency)
}

// Session implements slog.LogValuer as well as fmt.Stringer. LogValue is the
// one slog looks for, and it wins over String on both handlers here, because
// resolution happens before either handler formats anything: a compliant
// handler never sees the unresolved value at all.
//
// LogValue is also where redaction belongs: the Token field never reaches a
// handler because the resolved value does not contain it. Resolves counts the
// calls, which is how the contract below proves that resolution is lazy.
type Session struct {
	ID       string
	Token    string
	Resolves *atomic.Int64
}

func (s Session) String() string { return "session " + s.ID }

func (s Session) LogValue() slog.Value {
	s.Resolves.Add(1)
	return slog.GroupValue(slog.String("id", s.ID))
}

// GCPStyle is the ReplaceAttr most people end up writing: rename slog's keys
// to what the log backend expects and drop the timestamp the backend adds
// itself.
//
// The len(groups) > 0 guard is the part that gets left out. Without it, a user
// attribute that happens to be called "msg" inside a group gets renamed too,
// because ReplaceAttr is called for every non-group attribute at every depth,
// not only for the built-ins.
//
// The guard is not a cure. A "msg" attribute at the TOP level arrives with no
// open groups, exactly like the built-in one, so this replacer renames both and
// the record carries two "message" keys.
func GCPStyle(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		// A ZERO Attr is what drops a field, and the whole Attr has to be
		// zero. An empty key is not the rule: slog.String(a.Key, "") keeps
		// the field with an empty value, and slog.String("", v) writes a ""
		// key. Both are asserted, because the short version of this rule is
		// usually remembered as "return an empty key".
		return slog.Attr{}
	case slog.LevelKey:
		// The level arrives as a KindAny holding a slog.Level, not as a
		// string, because handlers pass slog.Any(slog.LevelKey, r.Level).
		if lvl, ok := a.Value.Any().(slog.Level); ok {
			return slog.String("severity", strings.ToLower(lvl.String()))
		}
	case slog.MessageKey:
		a.Key = "message"
	}
	return a
}

// ReplaceCall records one ReplaceAttr invocation.
type ReplaceCall struct {
	Groups string // open groups joined with "."
	Key    string
	Kind   slog.Kind
}

// Recorder returns a ReplaceAttr that changes nothing and writes down every
// call, so the call sequence itself can be asserted.
func Recorder(calls *[]ReplaceCall) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		*calls = append(*calls, ReplaceCall{
			Groups: strings.Join(groups, "."),
			Key:    a.Key,
			Kind:   a.Value.Kind(),
		})
		return a
	}
}

// HandlerStats counts the handler-contract calls a Logger makes.
type HandlerStats struct {
	Enabled   atomic.Int64
	Handled   atomic.Int64
	WithAttrs atomic.Int64
	WithGroup atomic.Int64
}

// CountingHandler is a pass-through middleware handler. It exists to make the
// Logger's side of the contract observable, and to be a correct example of the
// clone rule: WithAttrs and WithGroup build a new CountingHandler around the
// new inner handler. The counters are shared on purpose — they are the
// instrument, not handler state. Handler state must not be shared, which is
// exactly what the parent/sibling assertions check.
type CountingHandler struct {
	inner slog.Handler
	stats *HandlerStats
}

func NewCountingHandler(inner slog.Handler, stats *HandlerStats) *CountingHandler {
	return &CountingHandler{inner: inner, stats: stats}
}

func (h *CountingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.stats.Enabled.Add(1)
	return h.inner.Enabled(ctx, level)
}

func (h *CountingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.stats.Handled.Add(1)
	return h.inner.Handle(ctx, r)
}

func (h *CountingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.stats.WithAttrs.Add(1)
	return &CountingHandler{inner: h.inner.WithAttrs(attrs), stats: h.stats}
}

func (h *CountingHandler) WithGroup(name string) slog.Handler {
	h.stats.WithGroup.Add(1)
	return &CountingHandler{inner: h.inner.WithGroup(name), stats: h.stats}
}

// Emit writes one record straight through a handler, bypassing Logger, which
// is the only way to hand a handler a zero Record.Time. Handlers are required
// to omit the timestamp in that case, and it makes the output of a
// human-readable handler deterministic enough to assert on.
func Emit(h slog.Handler, level slog.Level, msg string, args ...any) error {
	r := slog.NewRecord(time.Time{}, level, msg, 0)
	r.Add(args...)
	return h.Handle(context.Background(), r)
}

// DecodeLines parses the JSON records written to buf, one object per line.
func DecodeLines(buf *bytes.Buffer) ([]map[string]any, error) {
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
}

// Lines returns the raw output lines, which is how key order and exact
// formatting get asserted.
func Lines(buf *bytes.Buffer) []string {
	s := strings.TrimSuffix(buf.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
