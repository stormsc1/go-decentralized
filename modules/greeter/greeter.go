// Package greeter says hello across the network. It's an example module: it
// runs compiled into a node, or in a process of its own (cmd/greeter), from
// the same code.
package greeter

import (
	"context"
	_ "embed"
	"fmt"

	"go-decentralized/module"
)

const Name = "greeter"

//go:embed module.yaml
var manifest []byte

// Config is the `config:` block of the greeter module in a node definition.
type Config struct {
	// Greeting starts every greeting. Defaults to "Hello".
	Greeting string `yaml:"greeting"`
}

type Module struct {
	cfg Config
	env module.Env
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	cfg := Config{Greeting: "Hello"}
	if err := decode(&cfg); err != nil {
		return nil, err
	}
	return &Module{cfg: cfg, env: env}, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"hello": module.HandlerFor(m.hello),
		"greet": module.HandlerFor(m.greet),
	}
}

type greeting struct {
	Greeting string `json:"greeting"`
}

func (m *Module) hello(ctx context.Context, in struct {
	Name string `json:"name"`
}) (greeting, error) {
	caller := module.Caller(ctx)
	if err := m.env.Emit(ctx, "greeted", map[string]string{"name": in.Name, "caller": caller}); err != nil {
		return greeting{}, err
	}
	return greeting{fmt.Sprintf("%s %s, from %s! You called from node %.8s.", m.cfg.Greeting, in.Name, m.env.NodeName, caller)}, nil
}

// greet asks the node with the given ID for a greeting.
func (m *Module) greet(ctx context.Context, in struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}) (greeting, error) {
	var out greeting
	err := m.env.CallNode(ctx, in.ID, Name+".hello", map[string]string{"name": in.Name}, &out)
	return out, err
}
