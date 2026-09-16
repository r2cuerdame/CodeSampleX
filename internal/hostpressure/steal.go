// Package hostpressure classifies host-level CPU contention -- the host VM
// not being scheduled by its hypervisor -- separately from ordinary
// application CPU load, by reading the steal-time field Linux's /proc/stat
// already tracks (see man 5 proc).
//
// This is deliberately host-level, not database-level: it says nothing
// about what this process is doing to PostgreSQL (internal/serverstore's
// query budget owns that signal), only whether the machine underneath this
// process is getting the CPU time its scheduler promised it. #454's
// governor (Task 6) uses this to tell "the host isn't getting scheduled"
// apart from "the app is busy" before deciding whether to shed load.
package hostpressure

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnsupportedPlatform is returned by Sample when /proc is not available
// on this OS -- every non-Linux platform, including the Windows machines
// this repository is developed on. A caller must treat this as "no signal,
// do not shed load on this basis", never as an alarm: a missing reading is
// not the same claim as a healthy one.
var ErrUnsupportedPlatform = errors.New("hostpressure: /proc is not available on this platform")

const (
	procStatPath    = "/proc/stat"
	procLoadAvgPath = "/proc/loadavg"
)

// procStatSample is one snapshot of /proc/stat's aggregate "cpu" line: ten
// monotonically increasing tick counters accumulated since boot.
type procStatSample struct {
	StealTicks uint64
	TotalTicks uint64
}

// parseProcStat reads the first line of an r shaped like /proc/stat -- the
// aggregate "cpu " line, documented in man 5 proc as ten whitespace-
// separated tick counts: user, nice, system, idle, iowait, irq, softirq,
// steal, guest, guest_nice. TotalTicks sums all ten; StealTicks is field
// index 7 (0-based, after the "cpu" label).
//
// guest and guest_nice are already counted inside user and nice by the
// kernel's own accounting, so summing all ten here double-counts them
// against a strict "ticks of wall-clock time elapsed" total. That is
// harmless here: TotalTicks is only ever used as the denominator of a ratio
// between two samples built the same way (see stealPercent), and a fixed
// double-count on both sides of that ratio cancels out.
func parseProcStat(r io.Reader) (procStatSample, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return procStatSample{}, fmt.Errorf("hostpressure: reading /proc/stat: %w", err)
		}
		return procStatSample{}, errors.New("hostpressure: /proc/stat is empty")
	}
	line := scanner.Text()
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "cpu" {
		return procStatSample{}, fmt.Errorf("hostpressure: /proc/stat first line does not start with %q: %q", "cpu ", line)
	}
	fields = fields[1:]
	// Through steal (index 7 of the ten documented fields); guest and
	// guest_nice were added later and are not required to be present on
	// every kernel this ever runs against.
	if len(fields) < 8 {
		return procStatSample{}, fmt.Errorf("hostpressure: /proc/stat cpu line has %d fields, want at least 8 (through steal)", len(fields))
	}
	var sample procStatSample
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return procStatSample{}, fmt.Errorf("hostpressure: /proc/stat cpu field %d (%q) is not a tick count: %w", i, f, err)
		}
		sample.TotalTicks += v
		if i == 7 {
			sample.StealTicks = v
		}
	}
	return sample, nil
}

// stealPercent is the share of the interval between prev and cur that went
// to steal time: (cur.StealTicks-prev.StealTicks) /
// (cur.TotalTicks-prev.TotalTicks) * 100.
//
// It returns 0 -- not a divide-by-zero panic, not a negative or inflated
// ratio -- whenever there is nothing sound to divide by: no wall-clock time
// elapsed between the two samples, or the counters moved backwards (a
// /proc/stat that did not actually advance, or a counter wraparound). 0 is
// the same answer parseProcStat gives a caller with no prior sample at all,
// which is the honest answer in both cases: no measured steal this tick.
func stealPercent(prev, cur procStatSample) float64 {
	if cur.TotalTicks <= prev.TotalTicks || cur.StealTicks < prev.StealTicks {
		return 0
	}
	deltaSteal := cur.StealTicks - prev.StealTicks
	deltaTotal := cur.TotalTicks - prev.TotalTicks
	return float64(deltaSteal) / float64(deltaTotal) * 100
}

// Reading is one point-in-time host-pressure measurement.
type Reading struct {
	// StealPercent is the share of CPU time since the previous Sample call
	// that the hypervisor took from this VM instead of scheduling it --
	// infrastructure contention, not application load. The first call on a
	// fresh Sampler has no prior sample to diff against and reports 0, not
	// "no steal observed" -- that distinction lives in whether an error
	// came back, not in this field.
	StealPercent float64
	// LoadAvg1 is /proc/loadavg's one-minute load average: ordinary
	// application-visible CPU and runnable-process demand, read fresh on
	// every call (it needs no prior sample).
	LoadAvg1 float64
	// SampledAt is when this reading was taken.
	SampledAt time.Time
}

// Sampler holds the previous /proc/stat sample so successive Sample calls
// can report steal time as a rate rather than a cumulative counter. It is
// safe for concurrent use; a process needs exactly one.
type Sampler struct {
	mu       sync.Mutex
	last     procStatSample
	haveLast bool
}

// NewSampler returns a Sampler with no prior reading.
func NewSampler() *Sampler {
	return &Sampler{}
}

// Sample reads /proc/stat and /proc/loadavg and returns a Reading.
//
// On any platform without /proc -- every non-Linux build, which is every
// developer machine this repository is built on -- it returns
// ErrUnsupportedPlatform rather than a zero-valued Reading that would look
// exactly like a real "0% steal, load 0" measurement. A read failure once
// /proc exists (permissions, an unreadable file, a malformed line) also
// returns a wrapped error rather than a partial or zero Reading; only a
// fully successful read updates the Sampler's internal state, so a failed
// call never corrupts the next diff.
func (s *Sampler) Sample() (Reading, error) {
	if runtime.GOOS != "linux" {
		return Reading{}, ErrUnsupportedPlatform
	}

	cur, err := readProcStat(procStatPath)
	if err != nil {
		return Reading{}, err
	}
	loadAvg1, err := readLoadAvg1(procLoadAvgPath)
	if err != nil {
		return Reading{}, err
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	var stealPct float64
	if s.haveLast {
		stealPct = stealPercent(s.last, cur)
	}
	s.last, s.haveLast = cur, true

	return Reading{StealPercent: stealPct, LoadAvg1: loadAvg1, SampledAt: now}, nil
}

func readProcStat(path string) (procStatSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return procStatSample{}, fmt.Errorf("hostpressure: opening %s: %w", path, err)
	}
	defer f.Close()
	sample, err := parseProcStat(f)
	if err != nil {
		return procStatSample{}, err
	}
	return sample, nil
}

func readLoadAvg1(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("hostpressure: opening %s: %w", path, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("hostpressure: %s is empty", path)
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("hostpressure: %s first field (%q) is not a number: %w", path, fields[0], err)
	}
	return v, nil
}
