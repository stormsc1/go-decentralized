// Package modules is the catalogue of modules a node definition can load.
package modules

import (
	"go-decentralized/internal/module"
	"go-decentralized/modules/debug"
	"go-decentralized/modules/discovery"
)

// Factories maps module names (as used in *.node.yaml) to their constructors.
var Factories = map[string]module.Factory{
	debug.Name:     debug.New,
	discovery.Name: discovery.New,
}
