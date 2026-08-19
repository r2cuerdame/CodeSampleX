package cli

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// The receipt's environment is stamped from the runner's ContainerOS, and an
// unset one reads as linux. So a machine serving Windows containers produced
// receipts claiming the stages ran on linux — a false environment, in a
// network whose entire product is that the environment recorded is the
// environment that ran. csx worker already asks the daemon; this path did not.
func TestSampleRunnerCarriesTheDaemonsContainerOS(t *testing.T) {
	old := sampleContainerOS
	defer func() { sampleContainerOS = old }()

	for _, want := range []string{sandbox.ContainerOSWindows, sandbox.ContainerOSLinux} {
		sampleContainerOS = func(context.Context) string { return want }
		runner := sampleRunner(context.Background(), domain.CapContainerRun)
		docker, ok := runner.(sandbox.DockerRunner)
		if !ok {
			t.Fatalf("CONTAINER_RUN gave %T, want a DockerRunner", runner)
		}
		if docker.ContainerOS != want {
			t.Errorf("ContainerOS = %q, want %q — the receipt would claim the wrong OS", docker.ContainerOS, want)
		}
	}
}

// Without a container there is no container OS to report.
func TestSampleRunnerStaysNativeWithoutContainers(t *testing.T) {
	if _, ok := sampleRunner(context.Background(), domain.CapCompileOnly).(sandbox.NativeRunner); !ok {
		t.Error("COMPILE_ONLY did not get the native runner")
	}
}

// The work request has to say what this machine can actually run. Claiming
// linux while serving Windows containers meant the server handed out linux
// work that the worker then executed — and stamped — as windows.
func TestSampleWorkerEnvelopeReportsTheRealContainerOS(t *testing.T) {
	oldOS, oldCap := sampleWorkerContainerOS, sampleWorkerCapability
	defer func() { sampleWorkerContainerOS, sampleWorkerCapability = oldOS, oldCap }()
	sampleWorkerCapability = func(context.Context) domain.SandboxCapability { return domain.CapContainerRun }

	for _, want := range []string{sandbox.ContainerOSWindows, sandbox.ContainerOSLinux} {
		sampleWorkerContainerOS = func(context.Context) string { return want }
		envelope := sampleWorkerEnvelope(context.Background())
		got, ok := envelope["verifierOS"].([]string)
		if !ok || len(got) != 1 {
			t.Fatalf("verifierOS = %#v", envelope["verifierOS"])
		}
		if got[0] != want {
			t.Errorf("verifierOS = %q, want %q", got[0], want)
		}
		if envelope["sandboxCapability"] != domain.CapContainerRun {
			t.Errorf("capability = %v", envelope["sandboxCapability"])
		}
	}
}
