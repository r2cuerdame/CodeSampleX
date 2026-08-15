package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate limiting exists because every write endpoint here is anonymous by
// design (goal.md §2.2: evidence carries no account). Anonymity removes the
// usual per-account quota, so the only remaining lever is the address the
// deployment's proxy observed — imperfect against a botnet, sufficient
// against the accident and the single abusive script that would otherwise
// fill a 60GB disk or exhaust a pool of 8 database connections.
//
// The limiter is in-process on purpose: one instance serves this network,
// and a Redis dependency would add a failure mode worth more than the
// accuracy it buys. If the deployment ever grows a second instance, the
// budgets below become per-instance rather than global — which fails safe.

// Per-class budgets, chosen from what an honest client actually does. A
// daemon uploads evidence every 15 minutes, syncs shards on demand, and
// publishes samples rarely; a browser reads pages continuously.
var (
	// writeLimit covers evidence, samples, verifications and announce.
	writeLimit = rate{burst: 60, per: time.Minute}
	// readLimit covers search and the shard/registry reads a warm-up storms.
	readLimit = rate{burst: 300, per: time.Minute}
	// authLimit covers the GitHub device flow: brute-forcing a device code
	// is the one thing here worth guessing at.
	authLimit = rate{burst: 20, per: time.Minute}
	// publishLimit covers sample upload alone. It is anonymous and durable
	// — the only write that creates permanent public content — and search
	// candidates are bounded, so a flood of samples can crowd out honest
	// ones. At the shared write budget that takeover cost about nine
	// minutes; at ten an hour it costs days, while no honest contributor
	// publishes ten samples in an hour.
	publishLimit = rate{burst: 10, per: time.Hour}
)

type rate struct {
	burst int           // tokens in a full bucket
	per   time.Duration // time to refill the whole bucket
}

// bucket is one client's token allowance for one class.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a keyed token bucket with lazy refill and periodic eviction.
// Lazy refill means an idle client costs one small struct and no timer.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    rate
	now     func() time.Time // test seam

	// lastSweep bounds memory: a flood of unique keys would otherwise grow
	// the map without limit, which is the very exhaustion being prevented.
	lastSweep time.Time
}

func newLimiter(r rate) *limiter {
	return &limiter{buckets: map[string]*bucket{}, rate: r}
}

func (l *limiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// allow takes one token for key. It returns false and the wait until the
// next token when the bucket is empty.
func (l *limiter) allow(key string) (bool, time.Duration) {
	refillPerSecond := float64(l.rate.burst) / l.rate.per.Seconds()
	now := l.clock()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.rate.burst), last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * refillPerSecond
		if b.tokens > float64(l.rate.burst) {
			b.tokens = float64(l.rate.burst)
		}
		b.last = now
	}
	if b.tokens < 1 {
		need := (1 - b.tokens) / refillPerSecond
		return false, time.Duration(need * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// sweepLocked drops buckets that have been untouched for longer than their
// refill period (l.rate.per). Any bucket idle this long has refilled
// completely, so forgetting it loses nothing.
func (l *limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > l.rate.per {
			delete(l.buckets, k)
		}
	}
}

// limiters holds one limiter per endpoint class.
type limiters struct {
	write, read, auth, publish *limiter
}

func newLimiters() *limiters {
	return &limiters{
		write:   newLimiter(writeLimit),
		read:    newLimiter(readLimit),
		auth:    newLimiter(authLimit),
		publish: newLimiter(publishLimit),
	}
}

// limit wraps h so each client gets its own budget for this class. Over
// budget answers 429 with Retry-After, never a silent drop: a client that
// cannot tell it was throttled retries immediately and makes it worse.
func (a *api) limit(l *limiter, h http.HandlerFunc) http.HandlerFunc {
	if l == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientAddr(r)
		ok, wait := l.allow(key)
		if !ok {
			seconds := int(wait.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeErr(w, http.StatusTooManyRequests,
				"rate limit exceeded; retry in "+strconv.Itoa(seconds)+"s")
			return
		}
		h(w, r)
	}
}
