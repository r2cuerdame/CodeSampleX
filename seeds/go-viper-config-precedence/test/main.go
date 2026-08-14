package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/viper"

	"codesamplex.dev/sample/goviper/src"
)

const document = `log:
  level: config
timeout: 30s
`

const kvDocument = `log:
  level: kv
`

// The env name viper computes for "log.level" under prefix "csx": uppercase
// the prefix, an underscore, then the key with the replacer applied.
const envName = "CSX_LOG_LEVEL"

func main() {
	precedence()
	flagDefaultIsLast()
	envNaming()
	emptyEnvIsNoEnv()
	bindEnvNaming()
	missingKeys()
	caseFolding()
	unmarshalSeesDifferentKeys()

	fmt.Println("contract ok")
}

// ladder builds a viper with every rung from top down to the default filled
// in, each writing the name of its own rung to the same key. Whatever Get
// returns is the name of the rung that won.
func ladder(top config.Level) *viper.Viper {
	v := config.New()
	v.SetDefault(config.Key, "default")
	if top <= config.LevelKV {
		must(config.ReadKeyValue(v, kvDocument))
	}
	if top <= config.LevelConfig {
		must(config.ReadDocument(v, document))
	}
	if top <= config.LevelEnv {
		must(os.Setenv(envName, "env"))
	} else {
		must(os.Unsetenv(envName))
	}
	if top <= config.LevelFlag {
		fs := config.FlagSet("flag default")
		must(fs.Parse([]string{"--log-level=flag"}))
		must(v.BindPFlag(config.Key, fs.Lookup("log-level")))
	}
	if top <= config.LevelSet {
		v.Set(config.Key, "set")
	}
	return v
}

func precedence() {
	for _, tc := range []struct {
		top  config.Level
		want string
	}{
		{config.LevelSet, "set"},
		{config.LevelFlag, "flag"},
		{config.LevelEnv, "env"},
		{config.LevelConfig, "config"},
		{config.LevelKV, "kv"},
		{config.LevelDefault, "default"},
	} {
		got := ladder(tc.top).GetString(config.Key)
		check(got == tc.want, "%v should outrank everything below it: got %q, want %q",
			tc.top, got, tc.want)
	}
	must(os.Unsetenv(envName))
}

// The rung that is not where people put it. A bound flag only enters the
// search at flag height once pflag records it as Changed; until then its
// default sits at the very bottom, below SetDefault.
func flagDefaultIsLast() {
	v := config.New()
	fs := config.FlagSet("flag default")
	must(v.BindPFlag(config.Key, fs.Lookup("log-level")))

	// Nothing else is set, so the flag default is all there is.
	check(v.GetString(config.Key) == "flag default", "unparsed flag: %q", v.GetString(config.Key))
	// IsSet searches with the flag-default pass switched off, so it disagrees
	// with Get about whether this key has a value. A guard written as
	// `if !v.IsSet(k) { return errors.New("required") }` rejects a key whose
	// Get would have returned the flag's default perfectly well.
	check(!v.IsSet(config.Key), "IsSet must ignore a flag default that was never changed")

	// A viper default outranks it, which is the wrong way round if you think
	// of the flag as living at flag height.
	v.SetDefault(config.Key, "default")
	check(v.GetString(config.Key) == "default", "SetDefault should beat an unchanged flag: %q",
		v.GetString(config.Key))
	check(v.IsSet(config.Key), "a default is enough for IsSet")

	// Parse the flag and it jumps the whole ladder.
	must(fs.Parse([]string{"--log-level=flag"}))
	check(v.GetString(config.Key) == "flag", "a changed flag should win: %q", v.GetString(config.Key))

	// The consequence, spelled out rather than inferred from the two steps
	// above: bind a flag, never parse it, and the config file buries its
	// default, even though the flag sits above the file in the documented order.
	buried := config.New()
	unparsed := config.FlagSet("flag default")
	must(buried.BindPFlag(config.Key, unparsed.Lookup("log-level")))
	must(config.ReadDocument(buried, document))
	check(buried.GetString(config.Key) == "config",
		"a config file should bury an unparsed flag's default: %q", buried.GetString(config.Key))
}

// AutomaticEnv does not look for CSX_LOG_LEVEL because someone decided it
// should. It builds the name mechanically, and the replacer is the only part
// of that name a dotted key can supply.
func envNaming() {
	bare := viper.New()
	bare.SetConfigType("yaml")
	bare.SetEnvPrefix(config.EnvPrefix)
	bare.AutomaticEnv()
	must(config.ReadDocument(bare, document))

	must(os.Setenv(envName, "env"))
	check(bare.GetString(config.Key) == "config",
		"without SetEnvKeyReplacer, %s is never consulted: %q", envName, bare.GetString(config.Key))

	// The name it does consult keeps the dot, which no shell can export. Go's
	// os.Setenv only refuses '=' and NUL, so the unreachable name is reachable
	// from here, and that is the proof the lookup happens at all.
	must(os.Setenv("CSX_LOG.LEVEL", "dotted"))
	check(bare.GetString(config.Key) == "dotted",
		"the computed name is uppercase(prefix + _ + key), dot intact: %q", bare.GetString(config.Key))
	must(os.Unsetenv("CSX_LOG.LEVEL"))

	// With the replacer installed, the spelling people expect is the spelling
	// that works.
	check(config.New().GetString(config.Key) == "env", "replacer should reach %s", envName)

	// The replacer runs over the finished name, prefix included: viper glues
	// the prefix on and uppercases, then replaces. A prefix carrying a
	// character the replacer rewrites gets rewritten too, which is easy to miss
	// when the replacer was added for the key's sake. Were it applied to the
	// key alone, the lookup here would be CSX.APP_LOG_LEVEL and find nothing.
	dottedPrefix := viper.New()
	dottedPrefix.SetEnvPrefix("csx.app")
	dottedPrefix.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	dottedPrefix.AutomaticEnv()
	must(os.Setenv("CSX_APP_LOG_LEVEL", "prefix replaced"))
	check(dottedPrefix.GetString(config.Key) == "prefix replaced",
		"the replacer rewrites the prefix too: %q", dottedPrefix.GetString(config.Key))
	must(os.Unsetenv("CSX_APP_LOG_LEVEL"))

	// The underscore between prefix and key is added by viper, so a prefix
	// written with one of its own doubles it.
	doubled := viper.New()
	doubled.SetEnvPrefix(config.EnvPrefix + "_")
	doubled.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	doubled.AutomaticEnv()
	must(os.Setenv("CSX__LOG_LEVEL", "doubled"))
	check(doubled.GetString(config.Key) == "doubled",
		"SetEnvPrefix(%q) should look up CSX__LOG_LEVEL: %q", config.EnvPrefix+"_", doubled.GetString(config.Key))
	must(os.Unsetenv("CSX__LOG_LEVEL"))
	must(os.Unsetenv(envName))
}

// An exported-but-empty variable is a real state for a shell, and viper
// throws it away unless told not to.
func emptyEnvIsNoEnv() {
	v := config.New()
	must(config.ReadDocument(v, document))
	must(os.Setenv(envName, ""))
	check(v.GetString(config.Key) == "config",
		"an empty env var falls through by default: %q", v.GetString(config.Key))

	allow := config.New()
	allow.AllowEmptyEnv(true)
	must(config.ReadDocument(allow, document))
	check(allow.GetString(config.Key) == "",
		"AllowEmptyEnv(true) makes the empty string a value: %q", allow.GetString(config.Key))
	check(allow.IsSet(config.Key), "and IsSet agrees it is set, even though the value is empty")

	// With nothing underneath to fall through to, the same difference decides
	// whether the key exists at all.
	must(os.Setenv("CSX_TRACE_ID", ""))
	check(!v.IsSet("trace.id"), "an env var exported empty leaves its key unset")
	check(allow.IsSet("trace.id"), "and set once empty values are allowed")
	must(os.Unsetenv("CSX_TRACE_ID"))
	must(os.Unsetenv(envName))
}

// BindEnv is the explicit half of the same machinery, and its two forms do
// not agree about the prefix.
func bindEnvNaming() {
	must(os.Setenv("CSX_PORT", "8080"))
	must(os.Setenv("SERVICE_PORT", "9090"))

	prefixed := viper.New()
	prefixed.SetEnvPrefix(config.EnvPrefix)
	must(prefixed.BindEnv("port"))
	// Values arrive from the environment as strings; the Get* family casts.
	check(prefixed.GetInt("port") == 8080, "one argument means prefix + key: %v", prefixed.Get("port"))

	explicit := viper.New()
	explicit.SetEnvPrefix(config.EnvPrefix)
	must(explicit.BindEnv("port", "SERVICE_PORT"))
	check(explicit.GetInt("port") == 9090,
		"a second argument is the env name verbatim: no prefix, no uppercasing, no replacer: %v",
		explicit.Get("port"))

	must(os.Unsetenv("CSX_PORT"))
	must(os.Unsetenv("SERVICE_PORT"))
}

// Nothing in the Get family returns an error, so a typo reads as a zero.
func missingKeys() {
	v := config.New()
	check(v.Get("nope") == nil, "Get on a missing key returns a nil any: %v", v.Get("nope"))
	check(v.GetString("nope") == "", "GetString: %q", v.GetString("nope"))
	check(v.GetInt("nope") == 0, "GetInt: %d", v.GetInt("nope"))
	check(!v.GetBool("nope"), "GetBool")
	check(v.GetDuration("nope") == 0, "GetDuration: %v", v.GetDuration("nope"))
	check(!v.IsSet("nope"), "IsSet is the only one that can tell you it was missing")

	// So a key really set to its zero value is indistinguishable from a typo
	// by Get alone, and IsSet is the whole answer.
	v.Set("retries", 0)
	check(v.GetInt("retries") == 0 && v.IsSet("retries"), "an explicit zero is set")

	// Measured, and worth knowing before writing "IsSet means the user chose
	// it": a key that only has a default is set. IsSet skips just one rung of
	// the search, the flag default, and defaults are not that rung.
	d := config.New()
	d.SetDefault("timeout", "30s")
	check(d.IsSet("timeout"), "IsSet is true for a key that only has a default")

	// The exception at the other end: Set(key, nil) stores nothing findable,
	// because nil is also how the search says "not here". It does not even
	// shadow the default underneath it.
	n := config.New()
	n.SetDefault("retries", 3)
	n.Set("retries", nil)
	check(n.GetInt("retries") == 3, "Set(key, nil) is invisible to the search: %v", n.Get("retries"))
}

func caseFolding() {
	v := config.New()
	v.Set("Feature.Flag", "ON")
	check(v.Get("feature.flag") == "ON", "lower: %v", v.Get("feature.flag"))
	check(v.Get("FEATURE.FLAG") == "ON", "upper: %v", v.Get("FEATURE.FLAG"))
	check(v.Get("Feature.Flag") == "ON", "as written: %v", v.Get("Feature.Flag"))
	// One key, stored once, under the folded spelling.
	check(slices.Contains(v.AllKeys(), "feature.flag"), "AllKeys: %v", v.AllKeys())
	check(!slices.Contains(v.AllKeys(), "Feature.Flag"), "AllKeys keeps no original spelling: %v", v.AllKeys())

	// Keys read from a document are folded on the way in. Values are not
	// touched, so only half of a case-sensitive comparison changes under you.
	mixed := config.New()
	must(config.ReadDocument(mixed, "Log:\n  Level: Config\n"))
	check(mixed.GetString(config.Key) == "Config", "keys fold, values do not: %q", mixed.GetString(config.Key))
}

// Get and Unmarshal ask different questions. Get searches the rungs for one
// key; Unmarshal enumerates AllKeys and reads each one. AutomaticEnv
// contributes no keys to that enumeration — it cannot, since it would have to
// guess names — so a value Get returns can be missing from the struct.
func unmarshalSeesDifferentKeys() {
	must(os.Setenv(envName, "env"))

	v := config.New()
	check(v.GetString(config.Key) == "env", "Get sees it: %q", v.GetString(config.Key))
	check(len(v.AllSettings()) == 0, "AllSettings does not: %v", v.AllSettings())

	var s config.Settings
	must(v.Unmarshal(&s))
	check(s.Log.Level == "", "Unmarshal walks AllKeys, so the field stays empty: %q", s.Log.Level)

	// Fix one: make the key known by any rung at all. The default never wins
	// the lookup — it only puts the key in AllKeys so Unmarshal asks for it.
	known := config.New()
	known.SetDefault(config.Key, "default")
	var s2 config.Settings
	must(known.Unmarshal(&s2))
	check(s2.Log.Level == "env", "a default makes the key enumerable: %q", s2.Log.Level)

	// Fix two: let viper take the key list from the destination struct.
	bound := config.New(viper.ExperimentalBindStruct())
	var s3 config.Settings
	must(bound.Unmarshal(&s3))
	check(s3.Log.Level == "env", "ExperimentalBindStruct adds the struct's keys: %q", s3.Log.Level)

	must(os.Unsetenv(envName))
}

func must(err error) {
	check(err == nil, "unexpected error: %v", err)
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
