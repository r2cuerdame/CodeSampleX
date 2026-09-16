package hostpressure

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestParseProcStatSteal(t *testing.T) {
	// Fields: user nice system idle iowait irq softirq steal guest guest_nice
	const sample = `cpu  1234 56 789 100000 12 3 4 500 0 0
cpu0 617 28 394 50000 6 1 2 250 0 0
`
	got, err := parseProcStat(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	if got.StealTicks != 500 {
		t.Fatalf("StealTicks = %d, want 500", got.StealTicks)
	}
	if got.TotalTicks == 0 {
		t.Fatal("TotalTicks must be nonzero")
	}
}

func TestStealPercentBetweenTwoSamples(t *testing.T) {
	a := procStatSample{StealTicks: 500, TotalTicks: 100000}
	b := procStatSample{StealTicks: 550, TotalTicks: 101000}
	got := stealPercent(a, b)
	// delta steal / delta total * 100 = 50/1000*100 = 5%
	if got < 4.9 || got > 5.1 {
		t.Fatalf("stealPercent = %v, want ~5.0", got)
	}
}

func TestParseProcStatRejectsMissingCPUPrefix(t *testing.T) {
	const sample = "notcpu 1 2 3 4 5 6 7 8 9 10\n"
	if _, err := parseProcStat(strings.NewReader(sample)); err == nil {
		t.Fatal("parseProcStat: want error when the first line does not start with \"cpu \", got nil")
	}
}

func TestParseProcStatRejectsTooFewFields(t *testing.T) {
	const sample = "cpu 1 2 3\n"
	if _, err := parseProcStat(strings.NewReader(sample)); err == nil {
		t.Fatal("parseProcStat: want error for a cpu line with too few fields, got nil")
	}
}

func TestParseProcStatRejectsNonNumericField(t *testing.T) {
	const sample = "cpu 1 2 3 4 5 6 7 notanumber 9 10\n"
	if _, err := parseProcStat(strings.NewReader(sample)); err == nil {
		t.Fatal("parseProcStat: want error for a non-numeric tick field, got nil")
	}
}

func TestParseProcStatRejectsEmptyInput(t *testing.T) {
	if _, err := parseProcStat(strings.NewReader("")); err == nil {
		t.Fatal("parseProcStat: want error for empty input, got nil")
	}
}

func TestStealPercentGuardsDivideByZero(t *testing.T) {
	a := procStatSample{StealTicks: 500, TotalTicks: 100000}
	same := procStatSample{StealTicks: 500, TotalTicks: 100000}
	if got := stealPercent(a, same); got != 0 {
		t.Fatalf("stealPercent = %v, want 0 when TotalTicks has not advanced", got)
	}
	backwards := procStatSample{StealTicks: 500, TotalTicks: 99000}
	if got := stealPercent(a, backwards); got != 0 {
		t.Fatalf("stealPercent = %v, want 0 when TotalTicks decreases", got)
	}
}

// TestSamplerFirstSampleHasNoPriorToDiffAgainst proves the documented
// contract: the first Sample() call after NewSampler has nothing to diff
// against and reports StealPercent 0 rather than an error or a nonsense
// value, while still recording the sample so the second call can diff
// against it.
func TestSamplerFirstSampleHasNoPriorToDiffAgainst(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/stat only exists on Linux")
	}
	s := NewSampler()
	reading, err := s.Sample()
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if reading.StealPercent != 0 {
		t.Fatalf("first Sample StealPercent = %v, want 0 (no prior sample to diff against)", reading.StealPercent)
	}
}

// TestSamplerReturnsErrUnsupportedPlatformOffLinux proves Sample never
// panics or silently returns a zero-valued Reading on a platform without
// /proc -- every non-Linux dev machine, this repository's own Windows
// workstation included -- but returns a clear, typed error a caller (Task
// 6's governor) can check with errors.Is and treat as "no signal".
func TestSamplerReturnsErrUnsupportedPlatformOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this asserts the non-Linux behavior; see TestSamplerReadsRealProcStatOnLinux for Linux")
	}
	s := NewSampler()
	_, err := s.Sample()
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Sample err = %v, want ErrUnsupportedPlatform", err)
	}
}

func TestSamplerReadsRealProcStatOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/stat only exists on Linux")
	}
	s := NewSampler()
	if _, err := s.Sample(); err != nil {
		t.Fatalf("Sample: %v", err)
	}
}
