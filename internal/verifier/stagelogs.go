package verifier

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StageLogStore keeps the stage output of a verification that failed.
//
// The receipt carries only a digest of these logs, deliberately and
// permanently: raw output holds paths, usernames and tokens, and none of it
// may leave the machine (goal.md §8.5). But the cross-verification worker
// then threw the logs away entirely, and its workspace is disposable, so a
// reproducible failure left nothing at all behind. Two peers hit the same
// Django 6.1 resolve failure a day apart; the sample was fine, the pinned
// hashes matched PyPI, and running the identical command in the identical
// image by hand exited 0 — and there was no way to see what differed,
// because the one artifact that would have said was discarded on the line
// between RunLogged and Run.
//
// Nothing here is uploaded. These files exist so an operator can read them on
// the machine that produced them.
type StageLogStore struct {
	// Home is CSX_HOME. Logs live in a subdirectory of it.
	Home string
	// MaxFiles bounds the directory. Zero uses defaultStageLogFiles.
	MaxFiles int
	// Now overrides the clock for tests.
	Now func() time.Time
}

const (
	stageLogDir          = "verify-logs"
	defaultStageLogFiles = 50
	// A single stage's output is truncated before it is written. A runaway
	// build can emit megabytes, and what diagnoses a failure is the end of
	// it; the cap is per stage so a noisy earlier stage cannot crowd out the
	// one that actually failed.
	maxStageLogBytes = 64 << 10
)

// Keep writes the stage logs for one verification if any stage failed, and
// returns the path it wrote. A run where nothing failed returns "" and writes
// nothing: there is nothing to diagnose and a worker fills a disk otherwise.
//
// An error here is never fatal to the caller. Logs are a diagnostic aid, not
// a precondition of verification, and a machine whose log directory is
// unwritable must still produce the evidence it was asked for.
func (s *StageLogStore) Keep(sampleID string, logs, stages map[string]string) (string, error) {
	if s == nil || s.Home == "" || len(logs) == 0 || !anyStageFailed(stages) {
		return "", nil
	}
	dir := filepath.Join(s.Home, stageLogDir)
	// 0700: the logs hold whatever the build printed, which is why they never
	// travel. On a shared machine they must not be readable by other accounts
	// either.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("verifier: stage logs: %w", err)
	}
	path := filepath.Join(dir, s.fileName(sampleID))
	if err := os.WriteFile(path, []byte(s.render(sampleID, logs, stages)), 0o600); err != nil {
		return "", fmt.Errorf("verifier: stage logs: %w", err)
	}
	s.prune(dir)
	return path, nil
}

// anyStageFailed reports whether the run has something worth diagnosing.
// SKIPPED is not a failure: it is what every stage after the failing one
// reports, and a run that skipped everything because it was never asked to do
// it has nothing to say.
func anyStageFailed(stages map[string]string) bool {
	for _, v := range stages {
		if strings.EqualFold(strings.TrimSpace(v), "FAIL") {
			return true
		}
	}
	return false
}

func (s *StageLogStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fileName carries the sample id and the time, so an operator reading a
// worker's stdout line can find the file weeks later.
func (s *StageLogStore) fileName(sampleID string) string {
	id := strings.TrimPrefix(sampleID, "sha256:")
	if len(id) > 16 {
		id = id[:16]
	}
	if id == "" {
		id = "unknown"
	}
	return fmt.Sprintf("%s-%s.log", s.now().UTC().Format("20060102T150405.000"), id)
}

func (s *StageLogStore) render(sampleID string, logs, stages map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "sample: %s\n", sampleID)
	fmt.Fprintf(&b, "at:     %s\n", s.now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "stages: %s\n\n", stageSummary(stages))
	fmt.Fprintf(&b, "These logs are local. They are not in the receipt and are never uploaded:\n"+
		"raw build output carries paths, usernames and tokens.\n")
	for _, stage := range sortedKeys(logs) {
		out := logs[stage]
		if len(out) > maxStageLogBytes {
			out = "… truncated, showing the last " +
				fmt.Sprint(maxStageLogBytes) + " bytes …\n" + out[len(out)-maxStageLogBytes:]
		}
		fmt.Fprintf(&b, "\n===== %s (%s) =====\n%s\n", stage, stageResult(stages, stage), out)
	}
	return b.String()
}

func stageSummary(stages map[string]string) string {
	parts := make([]string, 0, len(stages))
	for _, k := range sortedKeys(stages) {
		parts = append(parts, k+"="+stages[k])
	}
	return strings.Join(parts, " ")
}

func stageResult(stages map[string]string, stage string) string {
	if v, ok := stages[stage]; ok {
		return v
	}
	return "?"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// prune keeps the newest MaxFiles and deletes the rest. Names begin with a
// sortable UTC timestamp, so lexical order is chronological.
//
// Failure to prune is ignored on purpose: the log that was just written is
// more use than a tidy directory, and this runs on unattended workers.
func (s *StageLogStore) prune(dir string) {
	max := s.MaxFiles
	if max <= 0 {
		max = defaultStageLogFiles
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= max {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-max] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
