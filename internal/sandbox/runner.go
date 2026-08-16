// Package sandbox runs sample verification stages in isolation
// (goal.md §16). Downloaded samples never run directly on the host:
// with Docker they run in two-phase containers (resolve with network,
// everything after with --network=none); without Docker the native
// fallback resolves and compiles only and honestly SKIPs the contract.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// StageResult is one verification stage's outcome plus its local-only log.
type StageResult struct {
	Result string // PASS | FAIL | SKIPPED
	Log    string
}

// StageResult.Result values.
const (
	ResultPass    = "PASS"
	ResultFail    = "FAIL"
	ResultSkipped = "SKIPPED"
)

// Runner executes the three sandbox stages over an unpacked sample dir.
type Runner interface {
	Resolve(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	Build(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	Contract(ctx context.Context, dir string, m domain.SampleManifest) StageResult
	// StageEnvironment describes where the stages actually execute, given
	// the host environment. A receipt must name that environment, not the
	// host: a contract run inside a linux container proves nothing about
	// the Windows machine that started it.
	StageEnvironment(host domain.EnvironmentFingerprint, m domain.SampleManifest) domain.EnvironmentFingerprint
}

// stageTimeout bounds every stage (plan C13: 5m/stage).
const stageTimeout = 5 * time.Minute

// execCombined runs argv in dir and returns combined output. A package
// variable so unit tests can script executions without docker/npm.
var execCombined = func(ctx context.Context, dir string, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// runStage executes argv with the stage timeout and maps the exit status
// onto PASS/FAIL. The full command line and output stay in the local log.
func runStage(ctx context.Context, dir string, argv []string) StageResult {
	ctx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	out, err := execCombined(ctx, dir, argv)
	log := "$ " + strings.Join(argv, " ") + "\n" + strings.TrimSpace(string(out))
	if err != nil {
		return StageResult{Result: ResultFail, Log: log + "\nerror: " + err.Error()}
	}
	return StageResult{Result: ResultPass, Log: log}
}

func skipped(reason string) StageResult {
	return StageResult{Result: ResultSkipped, Log: reason}
}

// vendorDir is where every ecosystem's resolve output must land.
//
// Resolve and contract run as SEPARATE containers — resolve with the
// network, contract with --network=none — and only the workspace is
// mounted. Anything written outside it (an image's site-packages, the Go
// module cache, CARGO_HOME) is gone by the time the contract runs. npm
// happened to work because node_modules is already a workspace directory;
// every other ecosystem needed to be told, which is why only npm could
// reach contract verification.
const vendorDir = "/work/.csx-vendor"

// resolveCommand is the per-ecosystem dependency resolve step. Lifecycle
// scripts never run (--ignore-scripts / metadata-only fetches), and the
// output lands inside the workspace so the offline stages can see it.
func resolveCommand(ecosystem, runtime string) ([]string, error) {
	switch ecosystem {
	case "npm":
		switch runtime {
		case "bun":
			// Bun installs into the workspace's node_modules, so nothing
			// extra is needed for the offline stage to find it.
			return []string{"bun", "install", "--frozen-lockfile", "--ignore-scripts"}, nil
		case "deno":
			// `deno install` caches everything deno.json declares, which is
			// what the offline stage needs; `deno cache <file>` would only
			// cover the files named, and the runner cannot know which those
			// are. DENO_DIR is pointed inside the workspace by stageEnv, so
			// --cached-only then resolves with no network.
			return []string{"deno", "install"}, nil
		}
		return []string{"npm", "ci", "--ignore-scripts"}, nil
	case "pypi":
		// --target keeps the install in the workspace; stageEnv puts the
		// same path on PYTHONPATH for the contract stage.
		return []string{"pip", "install", "--no-deps", "--no-compile",
			"--target", vendorDir + "/py", "-r", "requirements.txt"}, nil
	case "golang":
		return []string{"go", "mod", "download"}, nil
	case "cargo":
		return []string{"cargo", "fetch"}, nil
	case "composer":
		// --no-scripts and --no-plugins are the composer equivalent of
		// npm's --ignore-scripts: composer.json is data, but scripts and
		// plugins in it are code, and they would run here with the network.
		return []string{"composer", "install", "--no-scripts", "--no-plugins",
			"--no-interaction", "--no-progress", "--prefer-dist"}, nil
	case "gem":
		// Bundler is not in the image's default gem set on every tag, and
		// installing it here keeps the contract stage free of network.
		//
		// The Gemfile check comes first because bundler's answer to a
		// missing one is to print its entire command list and exit 10.
		// Ninety-seven ruby samples out of a hundred and thirty died that
		// way, and what the author got back was thirty lines of "bundle
		// doctor / bundle env / bundle fund" with the actual reason
		// nowhere in it. A resolve that cannot say why it failed costs the
		// sample twice: once when it fails, and again when nobody can tell
		// what to change.
		return []string{"sh", "-c", gemResolveScript}, nil
	case "pub":
		return []string{"dart", "pub", "get"}, nil
	case "hex":
		// mix.exs is executable Elixir, and every mix task compiles and
		// evaluates it — the build.gradle.kts problem exactly. So the
		// resolve never lets mix see the project: it runs from a scratch
		// directory with no mix.exs (mix does not search parents), reads the
		// package set out of mix.lock by TEXT, and fetches each one with
		// hex.package. Never evaluate mix.lock with an Elixir evaluator;
		// that would reintroduce the execution this avoids.
		return []string{"sh", "-c", hexResolveScript}, nil
	}
	return nil, fmt.Errorf("sandbox: unsupported ecosystem %q", ecosystem)
}

// stageEnv points each toolchain's caches at the mounted workspace. The
// values are fixed constants pointing inside /work — no host environment
// is ever forwarded, so this narrows what the container sees rather than
// widening it.
func stageEnv(ecosystem, runtime string) []string {
	if ecosystem == "npm" && runtime == "deno" {
		// Deno's module cache lives outside the project by default, so it
		// would not survive the resolve container.
		return []string{"DENO_DIR=" + vendorDir + "/deno"}
	}
	switch ecosystem {
	case "composer":
		return []string{
			"COMPOSER_HOME=" + vendorDir + "/composer",
			"COMPOSER_CACHE_DIR=" + vendorDir + "/composer/cache",
		}
	case "gem":
		return []string{
			"GEM_HOME=" + vendorDir + "/gems",
			"GEM_PATH=" + vendorDir + "/gems",
			"BUNDLE_PATH=" + vendorDir + "/gems",
			"BUNDLE_APP_CONFIG=" + vendorDir + "/bundle",
		}
	case "pub":
		return []string{"PUB_CACHE=" + vendorDir + "/pub"}
	case "hex":
		// MIX_ENV must match in both stages: _build is per-env, so
		// resolving under one and testing under another silently rebuilds
		// and then fails with no network.
		return []string{
			"MIX_HOME=" + vendorDir + "/mix",
			"HEX_HOME=" + vendorDir + "/hex",
			"MIX_ENV=test",
		}
	case "pypi":
		return []string{"PYTHONPATH=" + vendorDir + "/py", "PYTHONDONTWRITEBYTECODE=1"}
	case "golang":
		return []string{
			"GOMODCACHE=" + vendorDir + "/gomod",
			"GOCACHE=" + vendorDir + "/gobuild",
			"GOFLAGS=-mod=mod",
		}
	case "cargo":
		return []string{
			"CARGO_HOME=" + vendorDir + "/cargo",
			"CARGO_TARGET_DIR=" + vendorDir + "/target",
		}
	}
	return nil
}

// hexResolveScript fetches the packages mix.lock pins without ever letting
// mix evaluate the sample's mix.exs. It requires the sample to ship a
// committed mix.lock — that file is the full transitive closure, so no
// resolver is needed, and a sample without one cannot be verified.
// The name and version are cut out with parameter expansion rather than a
// sed replacement. That is not a style choice: the sed form was written as
// 's/.../\1@\2/' and reached this file as 's/.../@/' after a round trip
// through tooling that ate the backreferences, so every fetch ran as
// `mix hex.package fetch "" ""`. The script still read correctly and failed
// only inside a container, which is the worst place to find out.
// gemResolveScript installs bundler and resolves, after saying plainly
// what is wrong when it cannot.
//
// Two diagnostics, both learned from a run where forty-two of forty-three
// verification failures were ruby and every one of them was unreadable:
//
//   - No Gemfile. Bundler answers that by printing its whole command list
//     and exiting 10, which tells an author nothing about what to write.
//   - A contract requiring minitest. GEM_PATH points at the vendor
//     directory ONLY, on purpose: a contract that passes because the base
//     image happened to ship a test framework proves something about the
//     image, not about the library. So minitest is genuinely absent, and
//     the author has to hear that from the resolve rather than from a
//     LoadError three stages later.
const gemResolveScript = `set -e
if [ ! -f Gemfile ]; then
  echo "csx: no Gemfile. A gem sample must pin its dependency in a Gemfile" >&2
  echo "csx: (with the exact version) so the resolve can be reproduced." >&2
  exit 1
fi
if grep -qE "require ['\"](minitest|rspec|test-unit)" test/*.rb 2>/dev/null; then
  if ! grep -qE "(minitest|rspec|test-unit)" Gemfile; then
    echo "csx: the contract requires a test framework that is not in the Gemfile." >&2
    echo "csx: GEM_PATH is the vendor directory only, so nothing the base image" >&2
    echo "csx: ships is visible. Assert with plain Ruby -- raise unless ... -- or" >&2
    echo "csx: declare and pin the framework as a dependency." >&2
    exit 1
  fi
fi
gem install bundler --no-document -q
bundle install --quiet`

const hexResolveScript = `set -e
mkdir -p ` + vendorDir + `/nomix /work/deps
cd ` + vendorDir + `/nomix
mix local.hex --force >/dev/null
mix local.rebar --force >/dev/null
for s in $(grep -oE ':hex, :[A-Za-z0-9_]+, "[^"]+"' /work/mix.lock | tr -d ' "'); do
  n=${s#:hex,:}
  n=${n%%,*}
  v=${s##*,}
  test -n "$n" && test -n "$v"
  mix hex.package fetch "$n" "$v" --unpack --output "/work/deps/$n"
done`
