// Package node loads a node definition and wires its modules together.
package node

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is a node definition, e.g. infra/gl-organization.node.yaml.
type Config struct {
	Version string         `yaml:"version"`
	Name    string         `yaml:"name"`
	Desc    string         `yaml:"desc"`
	Modules []ModuleConfig `yaml:"modules"`
}

// ModuleConfig names a module to load; Config is decoded by the module itself.
type ModuleConfig struct {
	Name   string    `yaml:"name"`
	Config yaml.Node `yaml:"config"`
}

// LoadConfig reads a node definition. ${VAR} and ${VAR:-default} references
// are expanded from the environment before parsing.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(path, raw)
}

// ParseConfig parses a node definition from raw bytes, see LoadConfig. name
// is only used in error messages.
func ParseConfig(name string, raw []byte) (Config, error) {
	var cfg Config
	expanded := os.Expand(string(raw), func(key string) string {
		name, def, hasDefault := strings.Cut(key, ":-")
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
		}
		if hasDefault {
			return def
		}
		return ""
	})
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", name, err)
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s: node name is required", name)
	}
	return cfg, nil
}
