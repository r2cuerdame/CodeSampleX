package lightsail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireDeployLockRecoversAbortedOwnerAndRejectsActiveOwner(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}

	baseProgram := deployHereString(t, "acquireDeployLock")
	const newOwner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const staleOwner = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	tests := []struct {
		name          string
		setup         func(t *testing.T, baseDir, homeDir, lockDir string)
		wantExit      int
		wantOwner     string
		wantAbortGone bool
	}{
		{
			name: "cold lock acquisition",
			setup: func(t *testing.T, baseDir, homeDir, lockDir string) {
				// No prior lock or abort file.
			},
			wantExit:  0,
			wantOwner: newOwner,
		},
		{
			name: "recovers aborted owner when abort marker exists and dir is clean",
			setup: func(t *testing.T, baseDir, homeDir, lockDir string) {
				if err := os.Mkdir(lockDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(staleOwner+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				abortFile := filepath.Join(homeDir, ".csx-deploy-aborted-"+staleOwner)
				if err := os.WriteFile(abortFile, []byte("aborted"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantExit:      0,
			wantOwner:     newOwner,
			wantAbortGone: true,
		},
		{
			name: "rejects lock when another deploy owns lock without abort marker",
			setup: func(t *testing.T, baseDir, homeDir, lockDir string) {
				if err := os.Mkdir(lockDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(staleOwner+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantExit:  73,
			wantOwner: staleOwner,
		},
		{
			name: "rejects lock when lock dir contains extra files",
			setup: func(t *testing.T, baseDir, homeDir, lockDir string) {
				if err := os.Mkdir(lockDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(staleOwner+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockDir, "extra_state"), []byte("active migration"), 0600); err != nil {
					t.Fatal(err)
				}
				abortFile := filepath.Join(homeDir, ".csx-deploy-aborted-"+staleOwner)
				if err := os.WriteFile(abortFile, []byte("aborted"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantExit:  73,
			wantOwner: staleOwner,
		},
		{
			name: "rejects lock when owner file is empty or malformed",
			setup: func(t *testing.T, baseDir, homeDir, lockDir string) {
				if err := os.Mkdir(lockDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte("../malicious/path\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 73,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			homeDir := filepath.Join(tmp, "home")
			if err := os.Mkdir(homeDir, 0700); err != nil {
				t.Fatal(err)
			}
			lockDir := filepath.Join(tmp, ".deploy-lock")

			tc.setup(t, tmp, homeDir, lockDir)

			program := strings.Replace(baseProgram, "lock=/opt/codesamplex/.deploy-lock", "lock="+filepath.ToSlash(lockDir), 1)
			program = strings.ReplaceAll(program, "__CSX_DEPLOY_OWNER__", newOwner)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, sh, "-c", program)
			cmd.Env = append(os.Environ(), "HOME="+filepath.ToSlash(homeDir))
			out, err := cmd.CombinedOutput()

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					t.Fatalf("unexpected execution error: %v", err)
				}
			}

			if exitCode != tc.wantExit {
				t.Fatalf("exit code = %d, want %d; output: %s", exitCode, tc.wantExit, string(out))
			}

			if tc.wantOwner != "" {
				ownerBytes, readErr := os.ReadFile(filepath.Join(lockDir, "owner"))
				if readErr != nil {
					t.Fatalf("failed to read owner file: %v", readErr)
				}
				gotOwner := strings.TrimSpace(string(ownerBytes))
				if gotOwner != tc.wantOwner {
					t.Fatalf("lock owner = %q, want %q", gotOwner, tc.wantOwner)
				}
			}

			if tc.wantAbortGone {
				abortFile := filepath.Join(homeDir, ".csx-deploy-aborted-"+staleOwner)
				if _, statErr := os.Stat(abortFile); !os.IsNotExist(statErr) {
					t.Fatalf("expected abort file %s to be removed, but still exists", abortFile)
				}
			}
		})
	}
}
