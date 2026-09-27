// Package node loads a node definition and wires its modules together.
package node

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"go-decentralized/internal/network"
)

// Config is a node definition.
type Config struct {
	Version string         `yaml:"version"`
	Name    string         `yaml:"name"`
	Desc    string         `yaml:"desc"`
	Network NetworkConfig  `yaml:"network"`
	Modules []ModuleConfig `yaml:"modules"`
}

// NetworkConfig configures how the node reaches, and finds, other nodes.
type NetworkConfig struct {
	// Bootstrap are addresses (host:port) of any nodes already in the
	// network, to join it through.
	Bootstrap []string `yaml:"bootstrap"`
	// Plaintext makes the node serve plain HTTP instead of dressing its
	// listener in TLS: for platforms that end TLS in front of it.
	Plaintext bool `yaml:"plaintext"`
	// MDNS advertises this node and finds others on the local network.
	// Defaults to true.
	MDNS  *bool               `yaml:"mdns"`
	Relay network.RelayConfig `yaml:"relay"`
}

// ModuleConfig names a module to load. Modules compiled into the node are
// found by name; others run in a process of their own, started with Run.
// Config is the module's own.
type ModuleConfig struct {
	Name   string    `yaml:"name"`
	Run    []string  `yaml:"run"`
	Config yaml.Node `yaml:"config"`
}

// configJSON returns the module's config as JSON, nil if it has none.
func (mc ModuleConfig) configJSON() (json.RawMessage, error) {
	if mc.Config.IsZero() {
		return nil, nil
	}
	var v any
	if err := mc.Config.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
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
