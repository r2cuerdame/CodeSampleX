package lightsail

import (
	"os/exec"
	"runtime"
	"testing"
)

func runFarmPhasePython(t *testing.T, file string) {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil || exec.Command(path, "-I", "-c", "import sys; assert sys.version_info >= (3, 8)").Run() != nil {
			continue
		}
		output, err := exec.Command(path, "-I", "-B", file, "-v").CombinedOutput()
		if err != nil {
			t.Fatalf("Farm SQL phase regression: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	t.Fatal("Python 3.8+ required for Farm SQL phase tests")
}

func TestBoundedFarmSQLPhases(t *testing.T) {
	runFarmPhasePython(t, "farm_sql_phases_test.py")
}

func TestFarmSQLPhasesDisposablePostgres17(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux canonical CI proves PostgreSQL 17 behavior in an isolated Docker fixture")
	}
	// Deliberately fail if Docker is unavailable on canonical Linux CI. This
	// proof must not become a silently skipped test or use CSX_TEST_DSN.
	runFarmPhasePython(t, "farm_sql_phases_pg_test.py")
}
