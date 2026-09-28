package module

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// A node starts a process module with ProtocolEnv set to the version of the
// protocol it speaks on the module's stdin and stdout. Version 1 is JSON-RPC
// 2.0, one message per line.
const (
	ProtocolEnv = "DECENTRALIZED_PROTOCOL"
	Protocol    = "1"
)

// Serve runs a module in a process of its own, for the node that started
// the process: it handles the calls the node sends on stdin, makes its own
// through the node, and returns once stdin closes. Nothing else may write
// to stdout, so Serve points os.Stdout at stderr, which the node logs. See
// spec/modules.md, "Process modules".
func Serve(newModule Factory) error {
	if v := os.Getenv(ProtocolEnv); v != Protocol {
		return fmt.Errorf("module: %s is %q, not %q: process modules are started by a node", ProtocolEnv, v, Protocol)
	}
	out := os.Stdout
	os.Stdout = os.Stderr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &server{ctx: ctx, newModule: newModule}
	s.link = NewLink(ctx, NewStdioStream(os.Stdin, out), s.handle)
	<-s.link.Done()
	cancel()
	s.wg.Wait()
	return nil
}

// server is the module's end of the link to its node.
type server struct {
	ctx       context.Context // until the node closes the link
	newModule Factory
	link      *Link
	wg        sync.WaitGroup

	mu       sync.Mutex
	module   Module
	manifest Manifest
	handlers map[string]Handler
}

// handle handles a call from the node: to the module runtime ("module.*"),
// or to one of the module's capabilities.
func (s *server) handle(ctx context.Context, call Call) (json.RawMessage, error) {
	switch call.Ref {
	case "module.start":
		return s.start(call.Input)
	case "module.inspect":
		s.mu.Lock()
		i, ok := s.module.(Inspector)
		s.mu.Unlock()
		if !ok {
			return Encode(nil)
		}
		return Encode(i.Inspect())
	}
	s.mu.Lock()
	manifest, handlers := s.manifest, s.handlers
	s.mu.Unlock()
	name, _ := strings.CutPrefix(call.Ref, manifest.Name+".")
	h := handlers[name]
	if h == nil {
		return nil, Errorf(CodeUnimplemented, "no capability %s", call.Ref)
	}
	ctx = WithCaller(ctx, call.From)
	if call.User != "" {
		ctx = WithUser(ctx, call.User)
	}
	out, err := h(ctx, func(v any) error { return Decode(call.Input, v) })
	if err != nil {
		return nil, err
	}
	return Encode(out)
}

type startInput struct {
	Node struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		DataDir string `json:"data_dir"`
	} `json:"node"`
	Config json.RawMessage `json:"config"`
}

// start constructs the module and returns its manifest.
func (s *server) start(input json.RawMessage) (json.RawMessage, error) {
	var in startInput
	if err := Decode(input, &in); err != nil {
		return nil, err
	}
	env := Env{
		NodeID:   in.Node.ID,
		NodeName: in.Node.Name,
		DataDir:  in.Node.DataDir,
		Call: func(ctx context.Context, ref string, in, out any) error {
			return s.call(ctx, Call{Ref: ref}, in, out)
		},
		CallNode: func(ctx context.Context, id, ref string, in, out any) error {
			return s.call(ctx, Call{Ref: ref, To: id}, in, out)
		},
		NotifyNode: func(ctx context.Context, id, ref string, in any) error {
			input, err := Encode(in)
			if err != nil {
				return err
			}
			return s.link.Notify(ctx, Call{Ref: ref, Input: input, To: id})
		},
		Emit: func(ctx context.Context, name string, body any) error {
			data, err := Encode(body)
			if err != nil {
				return err
			}
			return s.call(ctx, Call{Ref: "node.emit"}, map[string]any{"name": name, "body": data}, nil)
		},
		EmitTo: func(ctx context.Context, name string, body any, to []string) error {
			data, err := Encode(body)
			if err != nil {
				return err
			}
			return s.call(ctx, Call{Ref: "node.emit"}, map[string]any{"name": name, "body": data, "to": to}, nil)
		},
		Sign: func(ctx context.Context, purpose string, data []byte) ([]byte, error) {
			var out struct {
				Signature []byte `json:"signature"`
			}
			in := map[string]any{"purpose": purpose, "data": data}
			return out.Signature, s.call(ctx, Call{Ref: "node.sign"}, in, &out)
		},
	}
	m, err := s.newModule(ConfigDecoder(in.Config), env)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.module != nil {
		return nil, Errorf(CodeInvalidArgument, "already started")
	}
	s.module, s.manifest, s.handlers = m, m.Manifest(), m.Handlers()
	if r, ok := m.(Runner); ok {
		s.wg.Go(func() { r.Run(s.ctx) })
	}
	return Encode(map[string]any{"manifest": s.manifest})
}

// call makes a call through the node.
func (s *server) call(ctx context.Context, call Call, in, out any) error {
	input, err := Encode(in)
	if err != nil {
		return err
	}
	call.Input = input
	result, err := s.link.Call(ctx, call)
	if err != nil {
		return err
	}
	return Decode(result, out)
}

// ConfigDecoder returns a function that decodes a module's config, given as
// JSON, into v the way YAML would, e.g. "1m" into a time.Duration.
func ConfigDecoder(config json.RawMessage) func(v any) error {
	return func(v any) error {
		if len(config) == 0 {
			return nil
		}
		return yaml.Unmarshal(config, v)
	}
}
