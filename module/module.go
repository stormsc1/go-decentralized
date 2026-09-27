// Package module is the Go SDK for modules, the building blocks nodes are
// composed of. A module's module.yaml declares its capabilities and their
// schemas (see spec/modules.md), and its Go code handles them. Modules never
// see the network or how calls are encoded: the node carries them. The same
// module runs compiled into a node or in a process of its own, see Serve.
package module

import (
	"context"
	"encoding/json"
)

// Module is a module's implementation.
type Module interface {
	// Manifest describes the module, usually by parsing its module.yaml.
	Manifest() Manifest
	// Handlers handle the module's capabilities, by name: one for each
	// capability in the manifest.
	Handlers() map[string]Handler
}

// Handler handles calls to a capability. decode decodes the call's input
// into v. The node encodes the result.
type Handler func(ctx context.Context, decode func(v any) error) (any, error)

// HandlerFor adapts a typed function to a Handler.
func HandlerFor[In, Out any](h func(context.Context, In) (Out, error)) Handler {
	return func(ctx context.Context, decode func(any) error) (any, error) {
		var in In
		if err := decode(&in); err != nil {
			return nil, err
		}
		return h(ctx, in)
	}
}

// Runner is implemented by modules with background work. Run blocks until
// ctx is done.
type Runner interface {
	Run(ctx context.Context)
}

// Inspector is implemented by modules that report their state, for
// debugging (see the debug module). The state must encode as a JSON object.
type Inspector interface {
	Inspect() any
}

// Factory constructs a module. decode decodes the module's config block
// from the node definition into v, as YAML.
type Factory func(decode func(v any) error, env Env) (Module, error)

// Env is what a module gets from the node it runs in.
type Env struct {
	// NodeID identifies the node, see NodeID.
	NodeID string
	// NodeName is the name from the node definition.
	NodeName string
	// Call calls a capability of the node's own modules, by ref
	// ("<module>.<capability>"), and decodes its result into out.
	Call func(ctx context.Context, ref string, in, out any) error
	// CallNode calls a capability of the node with the given ID, and decodes
	// its result into out. The node finds the other node, and reaches it
	// directly or through a relay.
	CallNode func(ctx context.Context, id, ref string, in, out any) error
	// NotifyNode calls a capability of the node with the given ID like
	// CallNode, but doesn't wait for the call to end, nor learn how it did:
	// for news that may as well get lost, such as someone typing. It returns
	// once the call is sent.
	NotifyNode func(ctx context.Context, id, ref string, in any) error
	// Emit tells the node's subscribers, such as its users' apps, of the
	// module's event called name, which its manifest declares.
	Emit func(ctx context.Context, name string, body any) error
	// DataDir is the node's data directory, for files the module keeps of
	// its own; empty if the node keeps nothing on disk.
	DataDir string
	// Sign signs data with the node's key, for a purpose starting with the
	// module's name, e.g. "greeter.token". See Verify.
	Sign func(ctx context.Context, purpose string, data []byte) ([]byte, error)
}

type (
	callerKey   struct{}
	untracedKey struct{}
)

// Caller returns the ID of the node that made the call being handled: the
// ID it proved over TLS if it came from another node, or this node's own.
func Caller(ctx context.Context) string {
	id, _ := ctx.Value(callerKey{}).(string)
	return id
}

// WithCaller records who made a call, for Caller. Nodes call it.
func WithCaller(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callerKey{}, id)
}

// Untraced marks ctx so that the calls a native module makes with it, and
// the calls those lead to in the node, aren't recorded in the node's
// traces: for debugging tools' own traffic.
func Untraced(ctx context.Context) context.Context {
	return context.WithValue(ctx, untracedKey{}, true)
}

// IsUntraced reports whether ctx was marked by Untraced.
func IsUntraced(ctx context.Context) bool {
	return ctx.Value(untracedKey{}) != nil
}

// Encode encodes a call's input or result. Nil is an empty object.
func Encode(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("{}"), nil
	}
	return json.Marshal(v)
}

// Decode decodes a call's input or result into v, if v isn't nil.
func Decode(data json.RawMessage, v any) error {
	if v == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return Errorf(CodeInvalidArgument, "%v", err)
	}
	return nil
}
