package lightsail

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Compose reads .env for interpolation, but only values named under a service's
// environment reach the process. Check the rendered deployment configuration,
// rather than merely finding GOMAXPROCS somewhere in the source file.
func TestServerGoMaxProcsReachesContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Docker Compose is unavailable on this Windows test host")
		}
		t.Fatalf("Docker Compose is required to verify the rendered server environment: %v", err)
	}
	compose, err := filepath.Abs(filepath.Join("..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	emptyEnv := filepath.Join(t.TempDir(), "empty.env")
	if err := os.WriteFile(emptyEnv, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, setting, want string
	}{
		{"default matches production two-vcpu host", "", "2"},
		{"operator override reaches process", "1", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("docker", "compose", "--env-file", emptyEnv, "-f", compose, "config", "--format", "json")
			cmd.Env = make([]string, 0, len(os.Environ())+1)
			for _, variable := range os.Environ() {
				if !strings.HasPrefix(strings.ToUpper(variable), "CSX_SERVER_GOMAXPROCS=") {
					cmd.Env = append(cmd.Env, variable)
				}
			}
			if tc.setting != "" {
				cmd.Env = append(cmd.Env, "CSX_SERVER_GOMAXPROCS="+tc.setting)
			}
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("render Compose configuration: %v", err)
			}
			var config struct {
				Services map[string]struct {
					Environment map[string]string `json:"environment"`
				} `json:"services"`
			}
			if err := json.Unmarshal(output, &config); err != nil {
				t.Fatal(err)
			}
			if got := config.Services["server"].Environment["GOMAXPROCS"]; got != tc.want {
				t.Fatalf("rendered server GOMAXPROCS = %q, want %q", got, tc.want)
			}
		})
	}
}
