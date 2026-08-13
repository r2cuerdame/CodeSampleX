package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func init() {
	Register(Command{
		Name:    "config",
		Summary: "read or change settings: csx config get <key> | set <key> <value>",
		Run:     configMain,
	})
}

// configMain implements `csx config get <key>` and
// `csx config set <key> <value>` over $CSX_HOME/config.json. Keys are the
// config.json field names (mode, serverUrl, daemonPort, ...). Values are
// parsed as JSON when possible (numbers, booleans, arrays), else taken as
// strings — so `csx config set daemonPort 48619` just works.
func configMain(ctx context.Context, args []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: csx config get <key> | set <key> <value>")
		return 2
	}

	m, err := configAsMap(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	key := args[1]

	switch args[0] {
	case "get":
		v, ok := m[key]
		if !ok {
			fmt.Fprintf(os.Stderr, "csx: unknown config key %q (known: %s)\n", key, knownKeys(m))
			return 2
		}
		fmt.Println(renderValue(v))
		return 0

	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: csx config set <key> <value>")
			return 2
		}
		if _, ok := m[key]; !ok {
			fmt.Fprintf(os.Stderr, "csx: unknown config key %q (known: %s)\n", key, knownKeys(m))
			return 2
		}
		raw := args[2]
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			v = raw // not valid JSON: treat as a plain string
		}
		m[key] = v

		blob, err := json.Marshal(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		cfg := config.Default()
		if err := json.Unmarshal(blob, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "csx: invalid value for %q: %v\n", key, err)
			return 2
		}
		if err := cfg.Save(home); err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		fmt.Printf("%s = %s\n", key, renderValue(m[key]))
		return 0

	default:
		fmt.Fprintln(os.Stderr, "usage: csx config get <key> | set <key> <value>")
		return 2
	}
}

// configAsMap loads the effective config (defaults + file) as a JSON map.
func configAsMap(home string) (map[string]any, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// renderValue prints strings bare and everything else as JSON.
func renderValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(out)
}

func knownKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
