// Package module defines the building blocks every node is composed of:
// modules, which group related capabilities, and capabilities, which are the
// invokable units of work (addressed as "<module>.<capability>").
package module

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Args are the named arguments passed to a capability invocation.
type Args map[string]any

// Bool returns the boolean argument name, or def if it is absent or not a bool.
func (a Args) Bool(name string, def bool) bool {
	if v, ok := a[name].(bool); ok {
		return v
	}
	if s, ok := a[name].(string); ok {
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}

// String returns the argument name as a string, or "" if it is absent.
func (a Args) String(name string) string {
	if v, ok := a[name]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// Capability is a single invokable operation exposed by a module.
type Capability interface {
	Name() string
	Description() string
	Invoke(ctx context.Context, args Args) (any, error)
}

// Module groups a set of capabilities under a common name.
type Module interface {
	Name() string
	Capabilities() []Capability
}

// Registry holds the modules loaded into a node and resolves capability refs.
type Registry struct {
	modules map[string]Module
}

func NewRegistry() *Registry {
	return &Registry{modules: map[string]Module{}}
}

func (r *Registry) Register(m Module) error {
	if _, exists := r.modules[m.Name()]; exists {
		return fmt.Errorf("module %q already registered", m.Name())
	}
	r.modules[m.Name()] = m
	return nil
}

// Modules returns the registered modules sorted by name.
func (r *Registry) Modules() []Module {
	out := make([]Module, 0, len(r.modules))
	for _, m := range r.modules {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Refs returns every capability ref ("<module>.<capability>") in the registry.
func (r *Registry) Refs() []string {
	var refs []string
	for _, m := range r.Modules() {
		for _, c := range m.Capabilities() {
			refs = append(refs, m.Name()+"."+c.Name())
		}
	}
	return refs
}

// Resolve looks up a capability by its "<module>.<capability>" reference.
func (r *Registry) Resolve(ref string) (Capability, error) {
	modName, capName, ok := strings.Cut(ref, ".")
	if !ok {
		return nil, fmt.Errorf("invalid capability ref %q, expected <module>.<capability>", ref)
	}
	m, ok := r.modules[modName]
	if !ok {
		return nil, fmt.Errorf("unknown module %q", modName)
	}
	for _, c := range m.Capabilities() {
		if c.Name() == capName {
			return c, nil
		}
	}
	return nil, fmt.Errorf("module %q has no capability %q", modName, capName)
}

// Invoke resolves and runs a capability.
func (r *Registry) Invoke(ctx context.Context, ref string, args Args) (any, error) {
	c, err := r.Resolve(ref)
	if err != nil {
		return nil, err
	}
	if args == nil {
		args = Args{}
	}
	return c.Invoke(ctx, args)
}
