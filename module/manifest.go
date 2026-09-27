package module

import (
	"encoding/json"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Manifest describes a module: it's what module.yaml holds. See
// spec/modules.md.
type Manifest struct {
	// Name identifies the module in a node and prefixes its capabilities.
	Name         string       `yaml:"name" json:"name"`
	Version      string       `yaml:"version" json:"version"`
	Description  string       `yaml:"description" json:"description,omitempty"`
	Capabilities []Capability `yaml:"capabilities" json:"capabilities"`
	// Events are what the module tells subscribers as it happens, see
	// Env.Emit.
	Events []Event `yaml:"events" json:"events,omitempty"`
	// Entities are the types of records the module keeps in its node's
	// store.
	Entities []Entity `yaml:"entities" json:"entities,omitempty"`
	// Defs are schemas the other schemas refer to, as "#/$defs/<name>".
	Defs map[string]any `yaml:"$defs" json:"$defs,omitempty"`
}

// Event is something a module tells subscribers as it happens.
type Event struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Schema is the JSON Schema of the event's body, an object.
	Schema map[string]any `yaml:"schema" json:"schema,omitempty"`
}

// Entity is a type of record a module keeps in its node's store.
type Entity struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Schema is the JSON Schema of the records, which are objects.
	Schema map[string]any `yaml:"schema" json:"schema,omitempty"`
	// Indexes are the top-level fields records can be queried and sorted
	// by, besides their ID.
	Indexes []string `yaml:"indexes" json:"indexes,omitempty"`
}

// Capability is something a module does for callers.
type Capability struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Access says who may call it. Defaults to Local.
	Access Access `yaml:"access" json:"access,omitempty"`
	// Internal capabilities belong to a protocol between modules, e.g. the
	// DHT's: nodes don't announce them, and tools don't list them.
	Internal bool `yaml:"internal" json:"internal,omitempty"`
	// Input and Output are the JSON Schemas of the call's input and result,
	// both objects. Unset means any object.
	Input  map[string]any `yaml:"input" json:"input,omitempty"`
	Output map[string]any `yaml:"output" json:"output,omitempty"`
}

// Access says who may call a capability.
type Access string

const (
	// Local capabilities are for the node itself: its modules and its local
	// tools, such as the CLI.
	Local Access = "local"
	// Network capabilities are for other nodes too.
	Network Access = "network"
)

var (
	name  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	field = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseManifest parses a module.yaml.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	return m, m.Validate()
}

// MustParseManifest parses a module.yaml embedded in a module, and panics if
// it's invalid.
func MustParseManifest(data []byte) Manifest {
	m, err := ParseManifest(data)
	if err != nil {
		panic(err)
	}
	return m
}

// Validate checks the manifest's names, and that its schemas describe
// objects.
func (m Manifest) Validate() error {
	if !name.MatchString(m.Name) {
		return fmt.Errorf("manifest: invalid module name %q", m.Name)
	}
	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		if !name.MatchString(c.Name) || seen[c.Name] {
			return fmt.Errorf("manifest %s: invalid or repeated capability name %q", m.Name, c.Name)
		}
		seen[c.Name] = true
		if c.Access != "" && c.Access != Local && c.Access != Network {
			return fmt.Errorf("manifest %s: %s: access must be local or network", m.Name, c.Name)
		}
		for _, s := range []map[string]any{c.Input, c.Output} {
			if !object(s) {
				return fmt.Errorf("manifest %s: %s: inputs and outputs must be objects", m.Name, c.Name)
			}
		}
	}
	// Events share the capabilities' names, <module>.<name>.
	for _, e := range m.Events {
		if !name.MatchString(e.Name) || seen[e.Name] {
			return fmt.Errorf("manifest %s: invalid event name %q, or one a capability or event has", m.Name, e.Name)
		}
		seen[e.Name] = true
		if !object(e.Schema) {
			return fmt.Errorf("manifest %s: event %s: bodies must be objects", m.Name, e.Name)
		}
	}
	seen = map[string]bool{}
	for _, e := range m.Entities {
		if !name.MatchString(e.Name) || seen[e.Name] {
			return fmt.Errorf("manifest %s: invalid or repeated entity name %q", m.Name, e.Name)
		}
		seen[e.Name] = true
		if !object(e.Schema) {
			return fmt.Errorf("manifest %s: entity %s: records must be objects", m.Name, e.Name)
		}
		for _, f := range e.Indexes {
			if !field.MatchString(f) {
				return fmt.Errorf("manifest %s: entity %s: invalid index %q", m.Name, e.Name, f)
			}
		}
	}
	return nil
}

// object reports whether a schema describes objects, as inputs, outputs and
// records must be. An unset schema means any object.
func object(s map[string]any) bool {
	return s == nil || s["$ref"] != nil || s["type"] == "object"
}

// Entity returns the entity type called name.
func (m Manifest) Entity(name string) (Entity, bool) {
	for _, e := range m.Entities {
		if e.Name == name {
			return e, true
		}
	}
	return Entity{}, false
}

// Capability returns the capability called name.
func (m Manifest) Capability(name string) (Capability, bool) {
	for _, c := range m.Capabilities {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// Schema returns a standalone JSON Schema: s, with the manifest's defs.
func (m Manifest) Schema(s map[string]any) ([]byte, error) {
	schema := map[string]any{"type": "object"}
	for k, v := range s {
		schema[k] = v
	}
	if len(m.Defs) > 0 {
		schema["$defs"] = m.Defs
	}
	return json.Marshal(schema)
}
