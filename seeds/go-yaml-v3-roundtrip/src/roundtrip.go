// Package sample round-trips YAML config with gopkg.in/yaml.v3.
package sample

import "gopkg.in/yaml.v3"

// Config shows the tag rule: without a yaml tag, v3 matches the LOWERCASED
// field name — not the Go name and not encoding/json's camelCase. A field
// called MaxRetries therefore reads "maxretries", which is the usual cause
// of a config value silently staying zero.
type Config struct {
	Name       string   `yaml:"name"`
	Port       int      `yaml:"port"`
	Debug      bool     `yaml:"debug"`
	Tags       []string `yaml:"tags"`
	Retries    int      `yaml:"retries,omitempty"`
	MaxRetries int      // no tag: reads "maxretries"
}

func Load(text string) (Config, error) {
	var c Config
	err := yaml.Unmarshal([]byte(text), &c)
	return c, err
}

func Dump(c Config) (string, error) {
	b, err := yaml.Marshal(c)
	return string(b), err
}
