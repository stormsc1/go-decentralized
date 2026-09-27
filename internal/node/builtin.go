package node

import (
	"context"
	"encoding/json"
	"time"

	"go-decentralized/internal/network"
	"go-decentralized/module"
)

// builtins handle the node's own capabilities, see node.module.yaml.
func (n *Node) builtins() map[string]module.Handler {
	return map[string]module.Handler{
		"info": module.HandlerFor(func(context.Context, struct{}) (module.NodeInfo, error) {
			return n.Info(), nil
		}),
		"sign":    module.HandlerFor(n.signFor),
		"traces":  module.HandlerFor(n.traces),
		"inspect": module.HandlerFor(n.inspect),
	}
}

type signInput struct {
	Purpose string `json:"purpose"`
	Data    []byte `json:"data"`
}

type signOutput struct {
	Signature []byte `json:"signature"`
}

// signFor signs for the calling module, see sign.
func (n *Node) signFor(ctx context.Context, in signInput) (signOutput, error) {
	sig, err := n.sign(callingModule(ctx), in.Purpose, in.Data)
	return signOutput{Signature: sig}, err
}

type tracesInput struct {
	Since time.Time `json:"since"`
}

type tracesOutput struct {
	Traces []network.Trace `json:"traces"`
}

func (n *Node) traces(_ context.Context, in tracesInput) (tracesOutput, error) {
	return tracesOutput{Traces: n.Network.Traces(in.Since)}, nil
}

type inspectOutput struct {
	Network network.Status `json:"network"`
	Routing any            `json:"routing"`
	Modules map[string]any `json:"modules"`
}

// inspect collects the state of the network and of every module that
// reports one.
func (n *Node) inspect(ctx context.Context, _ struct{}) (inspectOutput, error) {
	out := inspectOutput{Network: n.Network.Status(), Routing: n.routing.Inspect(), Modules: map[string]any{}}
	for _, l := range n.loadedModules() {
		if i, ok := l.native.(module.Inspector); ok {
			out.Modules[l.manifest.Name] = i.Inspect()
		}
		if l.process != nil {
			if state, err := l.process.inspect(ctx); err == nil && string(state) != "{}" {
				out.Modules[l.manifest.Name] = json.RawMessage(state)
			}
		}
	}
	return out, nil
}
