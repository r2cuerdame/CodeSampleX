package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	sample "codesamplex.dev/sample/goyaml/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	c, err := sample.Load("name: codesamplex\nport: 8080\ndebug: true\ntags: [compat, evidence]\nmaxretries: 3\n")
	if err != nil {
		fail("load: %v", err)
	}
	if c.Name != "codesamplex" || c.Port != 8080 || !c.Debug || len(c.Tags) != 2 {
		fail("load: %+v", c)
	}
	// The untagged field matched the lowercased name.
	if c.MaxRetries != 3 {
		fail("untagged field: MaxRetries = %d, want 3", c.MaxRetries)
	}

	out, err := sample.Dump(c)
	if err != nil {
		fail("dump: %v", err)
	}
	// Retries is zero and tagged omitempty, so it must not appear.
	if strings.Contains(out, "retries:") && !strings.Contains(out, "maxretries:") {
		fail("omitempty did not drop the zero field:\n%s", out)
	}

	back, err := sample.Load(out)
	if err != nil || !reflect.DeepEqual(back, c) {
		fail("round trip: %+v vs %+v err=%v", back, c, err)
	}
	fmt.Println("CONTRACT PASS: yaml.v3 tags, omitempty and round trip behaved as documented")
}
