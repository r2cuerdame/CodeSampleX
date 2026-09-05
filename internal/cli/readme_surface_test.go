package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const readmeCLIStart = "<!-- BEGIN:CSX-CLI-SURFACE -->"
const readmeCLIEnd = "<!-- END:CSX-CLI-SURFACE -->"

var readmeCLICommand = regexp.MustCompile("(?m)^\\| \\x60([a-z-]+)\\x60 \\|")

func readmeCommands(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	a := strings.Index(s, readmeCLIStart)
	z := strings.Index(s, readmeCLIEnd)
	if a < 0 || z < 0 || z <= a {
		t.Fatalf("%s: CLI surface markers missing", path)
	}
	matches := readmeCLICommand.FindAllStringSubmatch(s[a:z], -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}
func TestReadmeCLISurfaceMatchesRegisteredCommands(t *testing.T) {
	want := make([]string, 0, len(Commands()))
	for _, c := range Commands() {
		if strings.HasPrefix(c.Name, "test-") {
			continue
		}
		want = append(want, c.Name)
	}

	for _, path := range []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs", "i18n", "README.ko.md"),
	} {
		got := readmeCommands(t, path)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s CLI surface drifted\n got: %v\nwant: %v", path, got, want)
		}
	}
}
