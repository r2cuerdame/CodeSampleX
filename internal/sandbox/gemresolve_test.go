package sandbox

import (
	"strings"
	"testing"
)

// Forty-two of forty-three verification failures in one run were ruby, and
// every one of them was unreadable: bundler answers a missing Gemfile by
// printing its whole command list and exiting 10, so the author saw
// "bundle doctor / bundle env / bundle fund" and no reason at all.
func TestGemResolveExplainsAMissingGemfile(t *testing.T) {
	argv, err := resolveCommand("gem", "ruby")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(argv, " ")
	if !strings.Contains(script, "no Gemfile") {
		t.Error("the resolve does not name the missing Gemfile")
	}
	// It has to say what to DO, not only what is wrong.
	if !strings.Contains(script, "pin its dependency") {
		t.Error("the resolve does not say what the author should write")
	}
	// And it must still resolve when the Gemfile is there.
	if !strings.Contains(script, "bundle install") {
		t.Error("the resolve no longer resolves")
	}
}

// GEM_PATH is the vendor directory only, deliberately: a contract that
// passes because the base image shipped minitest proves something about
// the image and nothing about the library. So the absence is by design and
// the author has to hear it from the resolve, not from a LoadError three
// stages later.
func TestGemResolveExplainsAnUndeclaredTestFramework(t *testing.T) {
	argv, _ := resolveCommand("gem", "ruby")
	script := strings.Join(argv, " ")
	for _, want := range []string{"minitest", "rspec", "GEM_PATH", "raise unless"} {
		if !strings.Contains(script, want) {
			t.Errorf("the resolve never mentions %q", want)
		}
	}
}

// The hermetic gem path is the reason the message above is needed, so it
// must not quietly widen to include the image's own gems.
func TestGemPathStaysHermetic(t *testing.T) {
	for _, e := range stageEnv("gem", "ruby") {
		if strings.HasPrefix(e, "GEM_PATH=") {
			if strings.Contains(e, ":") && !strings.Contains(e, "C:") {
				t.Errorf("GEM_PATH gained a second entry (%q); a contract that "+
					"passes on the image's own gems proves the wrong thing", e)
			}
			if !strings.Contains(e, vendorDir) {
				t.Errorf("GEM_PATH = %q, want it inside the workspace", e)
			}
			return
		}
	}
	t.Error("no GEM_PATH is set for the gem ecosystem")
}

// bundler and rubygems must agree on where a gem lives.
//
// BUNDLE_PATH=<dir> installs to <dir>/ruby/<abi>/gems/x while GEM_PATH
// =<dir> reads <dir>/gems/x, and the contract command is whatever the
// manifest declares — "ruby test/contract.rb", never "bundle exec" — so
// it went through rubygems, looked in the second path and raised LoadError
// for a gem resolved four directories away. Every ruby sample requiring a
// real gem failed that way; only default gems compiled into ruby itself
// ever passed.
func TestBundlerInstallsWhereRubygemsLooks(t *testing.T) {
	env := stageEnv("gem", "ruby")
	var home, path string
	var system bool
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "GEM_HOME="):
			home = strings.TrimPrefix(e, "GEM_HOME=")
		case strings.HasPrefix(e, "GEM_PATH="):
			path = strings.TrimPrefix(e, "GEM_PATH=")
		case e == "BUNDLE_PATH__SYSTEM=true":
			system = true
		case strings.HasPrefix(e, "BUNDLE_PATH="):
			t.Errorf("BUNDLE_PATH is set (%q): it nests the install under "+
				"ruby/<abi>/, which is not where GEM_PATH reads", e)
		}
	}
	if !system {
		t.Error("BUNDLE_PATH__SYSTEM is not set, so bundler will nest the install")
	}
	if home == "" || home != path {
		t.Errorf("GEM_HOME=%q GEM_PATH=%q: bundler installs into the first "+
			"and ruby reads the second, so they have to be the same", home, path)
	}
}
