// Package config wires spf13/viper the way a program does, and exposes the
// pieces needed to watch its precedence ladder decide a value.
//
// The documented order is: explicit Set, then flag, then environment, then
// config file, then key/value store, then default. What that sentence hides
// is that "flag" means a flag the user actually changed. viper checks
// flag.HasChanged() near the top of the search, and consults the flag's
// *default* only after everything else has failed — below SetDefault. So a
// flag default never overrides a config file, which is the opposite of what
// most people assume when they wire BindPFlag once and move on.
//
// Nothing here reads a path from disk. SetConfigType plus ReadConfig(io.Reader)
// is the whole file-loading path minus the file, which is also the cheapest
// way to test config handling without a fixture directory.
package config

import (
	"bytes"
	"io"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Key is the key every rung of the ladder writes. It is dotted on purpose: a
// dot is legal in a viper key and no shell can export a variable whose name
// contains one, and that mismatch is where the env lookup silently stops
// working.
const Key = "log.level"

// EnvPrefix is glued to the key as strings.ToUpper(prefix + "_" + key), so
// the prefix is written without its own trailing underscore.
const EnvPrefix = "csx"

// Level names one rung, highest priority first.
type Level int

const (
	LevelSet Level = iota
	LevelFlag
	LevelEnv
	LevelConfig
	LevelKV
	LevelDefault
)

func (l Level) String() string {
	return [...]string{"Set", "flag", "env", "config file", "key/value store", "default"}[l]
}

// New returns the viper an application ends up with: a format for readers
// that have no filename, a prefix so the process cannot be steered by a bare
// PATH-like name, a replacer so dotted keys have a legal env spelling, and
// AutomaticEnv so every Get consults the environment.
//
// Order does not matter among these four — each sets one field that is read
// at lookup time, not at call time — but all four are load-bearing: drop the
// replacer and "log.level" is looked up as CSX_LOG.LEVEL.
func New(opts ...viper.Option) *viper.Viper {
	v := viper.NewWithOptions(opts...)
	v.SetConfigType("yaml")
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return v
}

// ReadDocument fills the config-file rung from memory. ReadConfig needs
// SetConfigType because there is no extension to guess from.
func ReadDocument(v *viper.Viper, document string) error {
	return v.ReadConfig(bytes.NewBufferString(document))
}

// FlagSet is the flag rung. The flag is spelled log-level and the viper key
// is log.level; BindPFlag is the translation between them, which is why the
// two spellings never have to agree.
func FlagSet(defaultValue string) *pflag.FlagSet {
	fs := pflag.NewFlagSet("csxtool", pflag.ContinueOnError)
	fs.String("log-level", defaultValue, "logging level")
	return fs
}

// offlineKV stands in for etcd or consul. viper reaches its key/value rung
// through the package-level viper.RemoteConfig hook, whose interface type is
// unexported but whose three methods are not — so any type carrying them can
// be plugged in, and the rung nobody ever sees becomes observable without a
// server. Being package-level, this affects every *Viper in the process.
type offlineKV struct{ document string }

func (k offlineKV) Get(viper.RemoteProvider) (io.Reader, error) {
	return strings.NewReader(k.document), nil
}

func (k offlineKV) Watch(viper.RemoteProvider) (io.Reader, error) {
	return strings.NewReader(k.document), nil
}

func (k offlineKV) WatchChannel(viper.RemoteProvider) (<-chan *viper.RemoteResponse, chan bool) {
	return nil, nil
}

// ReadKeyValue fills the key/value rung. The payload is parsed with the same
// codec as the config file, so SetConfigType has to be in place first.
func ReadKeyValue(v *viper.Viper, document string) error {
	viper.RemoteConfig = offlineKV{document: document}
	if err := v.AddRemoteProvider("etcd3", "http://127.0.0.1:2379", "/csx"); err != nil {
		return err
	}
	return v.ReadRemoteConfig()
}

// Settings is the struct half of the API. Unmarshal builds its input from
// AllKeys, which is a different question than the one Get answers.
type Settings struct {
	Log struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"log"`
}
