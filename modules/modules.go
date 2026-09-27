// Package modules is the catalogue of modules compiled into nodes, which a
// node definition loads by name.
package modules

import (
	"go-decentralized/module"
	"go-decentralized/modules/debug"
	"go-decentralized/modules/greeter"
)

// Factories maps module names (as used in *.node.yaml) to their constructors.
var Factories = map[string]module.Factory{
	debug.Name:   debug.New,
	greeter.Name: greeter.New,
}
