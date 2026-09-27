package node

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"go-decentralized/internal/network"
	"go-decentralized/internal/routing"
	"go-decentralized/internal/store"
	"go-decentralized/module"
)

// Node is a running instance of a node definition: its network and its
// modules. Every call, whether from another node, from a module or from a
// local tool, goes through one dispatcher, which checks who may call what
// and validates inputs against the modules' schemas.
type Node struct {
	Config  Config
	ID      string
	Network *network.Network
	routing *routing.Routing
	stores  map[string]store.Store // the node's stores, by name, see Config.Stores
	key     ed25519.PrivateKey

	// Process modules may call while later modules load.
	mu       sync.RWMutex
	modules  []*loaded
	caps     map[string]*capability        // by ref
	events   map[string]*jsonschema.Schema // their bodies' schemas, by ref
	entities map[string]entityType         // by "<module>.<entity>"

	smu  sync.Mutex
	subs map[*Subscription]struct{}
}

// loaded is a module the node runs.
type loaded struct {
	manifest module.Manifest
	runtime  string             // "builtin", "native" or "process"
	native   module.Module      // native modules only
	process  *process           // process modules only
	config   json.RawMessage    // the module's block in the node definition
	bindings map[string]string  // the node's stores for the module's, from the node definition
	stores   map[string]binding // the module's stores, by its names for them
}

// binding is where one of a module's stores lives.
type binding struct {
	store string // the node's store it's bound to
	kind  string
}

// handler handles calls to a capability: input in, result out.
type handler func(ctx context.Context, body json.RawMessage) (json.RawMessage, error)

type capability struct {
	module *loaded
	spec   module.Capability
	input  *jsonschema.Schema
	handle handler
}

//go:embed node.module.yaml
var nodeManifest []byte

// New opens the node's stores and starts every module listed in cfg: native
// ones from factories, and process ones by running their command. Calls
// from other nodes arrive through nw.
func New(cfg Config, key ed25519.PrivateKey, nw *network.Network, factories map[string]module.Factory) (_ *Node, err error) {
	n := &Node{
		Config:   cfg,
		ID:       module.NodeID(key.Public().(ed25519.PublicKey)),
		Network:  nw,
		key:      key,
		caps:     map[string]*capability{},
		events:   map[string]*jsonschema.Schema{},
		entities: map[string]entityType{},
		subs:     map[*Subscription]struct{}{},
	}
	defer func() {
		if err != nil {
			n.close()
		}
	}()
	if err := n.openStores(); err != nil {
		return nil, fmt.Errorf("node %q: %w", cfg.Name, err)
	}
	if err := n.add(&loaded{manifest: module.MustParseManifest(nodeManifest), runtime: "builtin"}, natives(n.builtins())); err != nil {
		return nil, err
	}
	if err := n.add(&loaded{manifest: nw.Manifest(), runtime: "builtin"}, natives(nw.Handlers())); err != nil {
		return nil, err
	}
	n.routing, err = routing.New(routing.Config{
		Key:       key,
		Name:      cfg.Name,
		Network:   nw,
		Bootstrap: cfg.Network.Bootstrap,
		MDNS:      cfg.Network.MDNS == nil || *cfg.Network.MDNS,
		Provides:  n.provides,
		Memory:    n.local("routing"),
	})
	if err != nil {
		return nil, err
	}
	if err := n.add(&loaded{manifest: n.routing.Manifest(), runtime: "builtin"}, natives(n.routing.Handlers())); err != nil {
		return nil, err
	}
	if err := n.add(&loaded{manifest: module.MustParseManifest(storeManifest), runtime: "builtin"}, natives(n.storeHandlers())); err != nil {
		return nil, err
	}
	for _, mc := range cfg.Modules {
		if err := n.load(mc, factories); err != nil {
			return nil, fmt.Errorf("node %q: module %q: %w", cfg.Name, mc.Name, err)
		}
	}
	nw.SetHandler(n.serve)
	return n, nil
}

// load starts a module from the node definition.
func (n *Node) load(mc ModuleConfig, factories map[string]module.Factory) error {
	if mc.Name == "module" {
		return fmt.Errorf("the name is reserved")
	}
	config, err := mc.configJSON()
	if err != nil {
		return err
	}
	var l *loaded
	if len(mc.Run) > 0 {
		p, err := n.startProcess(mc.Name, mc.Run, config)
		if err != nil {
			return err
		}
		l = &loaded{manifest: p.manifest, runtime: "process", process: p}
	} else {
		factory, ok := factories[mc.Name]
		if !ok {
			return fmt.Errorf("no such module")
		}
		m, err := factory(module.ConfigDecoder(config), n.env(mc.Name))
		if err != nil {
			return err
		}
		l = &loaded{manifest: m.Manifest(), runtime: "native", native: m}
	}
	l.config, l.bindings = config, mc.Stores
	if l.manifest.Name != mc.Name {
		err = fmt.Errorf("the module calls itself %q", l.manifest.Name)
	} else if l.process != nil {
		err = n.add(l, l.process.handlers())
	} else {
		err = n.add(l, natives(l.native.Handlers()))
	}
	if err != nil && l.process != nil {
		l.process.stop()
	}
	return err
}

// add routes calls to a module's capabilities to its handlers.
func (n *Node) add(l *loaded, handlers map[string]handler) error {
	m := l.manifest
	if err := m.Validate(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, other := range n.modules {
		if other.manifest.Name == m.Name {
			return fmt.Errorf("two modules called %q", m.Name)
		}
	}
	for name := range handlers {
		if _, ok := m.Capability(name); !ok {
			return fmt.Errorf("%s handles %q, which its manifest doesn't declare", m.Name, name)
		}
	}
	caps := map[string]*capability{}
	for _, c := range m.Capabilities {
		h := handlers[c.Name]
		if h == nil {
			return fmt.Errorf("%s declares %q, but doesn't handle it", m.Name, c.Name)
		}
		input, err := compile(m, c.Input)
		if err != nil {
			return fmt.Errorf("%s.%s: input schema: %w", m.Name, c.Name, err)
		}
		caps[m.Name+"."+c.Name] = &capability{module: l, spec: c, input: input, handle: h}
	}
	events := map[string]*jsonschema.Schema{}
	for _, e := range m.Events {
		schema, err := compile(m, e.Schema)
		if err != nil {
			return fmt.Errorf("%s event %s: schema: %w", m.Name, e.Name, err)
		}
		events[m.Name+"."+e.Name] = schema
	}
	// The module's block in the node definition must be what the module
	// says it takes.
	if m.Config != nil {
		schema, err := compile(m, m.Config)
		if err != nil {
			return fmt.Errorf("%s: config schema: %w", m.Name, err)
		}
		block := l.config
		if len(block) == 0 {
			block = json.RawMessage("{}")
		}
		if err := validate(schema, block); err != nil {
			return fmt.Errorf("%s: config: %v", m.Name, err)
		}
	}
	entities, err := n.bindStores(l)
	if err != nil {
		return err
	}
	maps.Copy(n.caps, caps)
	maps.Copy(n.events, events)
	maps.Copy(n.entities, entities)
	n.modules = append(n.modules, l)
	return nil
}

// module returns the module called name, if the node runs it.
func (n *Node) module(name string) *loaded {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, l := range n.modules {
		if l.manifest.Name == name {
			return l
		}
	}
	return nil
}

// loadedModules returns the modules the node runs, in the order loaded.
func (n *Node) loadedModules() []*loaded {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return slices.Clone(n.modules)
}

// Call calls a capability of this node, as the node itself: for local tools
// such as the CLI and the local HTTP API.
func (n *Node) Call(ctx context.Context, ref string, body json.RawMessage) (json.RawMessage, error) {
	return n.call(ctx, n.ID, ref, body)
}

// serve handles a call from another node.
func (n *Node) serve(ctx context.Context, ref string, body json.RawMessage) (json.RawMessage, error) {
	return n.call(ctx, network.RemoteID(ctx), ref, body)
}

// call dispatches a call from the node caller.
func (n *Node) call(ctx context.Context, caller, ref string, body json.RawMessage) (json.RawMessage, error) {
	n.mu.RLock()
	c := n.caps[ref]
	n.mu.RUnlock()
	if c == nil {
		return nil, module.Errorf(module.CodeUnimplemented, "no capability %s", ref)
	}
	if caller != n.ID && c.spec.Access != module.Network {
		return nil, module.Errorf(module.CodePermissionDenied, "%s is only for this node", ref)
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.Equal(body, []byte("null")) {
		body = json.RawMessage("{}")
	}
	if err := validate(c.input, body); err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%s: %v", ref, err)
	}
	return c.handle(module.WithCaller(ctx, caller), body)
}

// natives adapts native handlers: they decode inputs and return results as
// Go values, which the node encodes.
func natives(hs map[string]module.Handler) map[string]handler {
	out := map[string]handler{}
	for name, h := range hs {
		out[name] = func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
			result, err := h(ctx, func(v any) error { return module.Decode(body, v) })
			if err != nil {
				return nil, err
			}
			return module.Encode(result)
		}
	}
	return out
}

type moduleKey struct{}

// callingModule returns the name of the module making the call being
// handled, if a module makes it.
func callingModule(ctx context.Context) string {
	name, _ := ctx.Value(moduleKey{}).(string)
	return name
}

// env is what a native module gets from the node.
func (n *Node) env(name string) module.Env {
	return module.Env{
		NodeID:   n.ID,
		NodeName: n.Config.Name,
		DataDir:  n.Config.DataDir,
		Call: func(ctx context.Context, ref string, in, out any) error {
			body, err := module.Encode(in)
			if err != nil {
				return err
			}
			result, err := n.call(context.WithValue(ctx, moduleKey{}, name), n.ID, ref, body)
			if err != nil {
				return err
			}
			return module.Decode(result, out)
		},
		CallNode: func(ctx context.Context, id, ref string, in, out any) error {
			body, err := module.Encode(in)
			if err != nil {
				return err
			}
			result, err := n.callNode(context.WithValue(ctx, moduleKey{}, name), id, ref, body)
			if err != nil {
				return err
			}
			return module.Decode(result, out)
		},
		NotifyNode: func(ctx context.Context, id, ref string, in any) error {
			body, err := module.Encode(in)
			if err != nil {
				return err
			}
			return n.notifyNode(context.WithValue(ctx, moduleKey{}, name), id, ref, body)
		},
		Emit: func(_ context.Context, event string, body any) error {
			data, err := module.Encode(body)
			if err != nil {
				return err
			}
			return n.emit(name, event, data)
		},
		Sign: func(_ context.Context, purpose string, data []byte) ([]byte, error) {
			return n.sign(name, purpose, data)
		},
	}
}

// callNode calls the capability ref of the node id, as this node: over its
// session with the node, or wherever routing finds it. Calls to this node
// itself stay local.
func (n *Node) callNode(ctx context.Context, id, ref string, body json.RawMessage) (json.RawMessage, error) {
	if id == n.ID {
		return n.call(ctx, n.ID, ref, body)
	}
	peer, err := n.peer(ctx, id)
	if err != nil {
		return nil, err
	}
	return n.Network.Call(ctx, peer, ref, body)
}

// notifyNode calls like callNode, but doesn't wait for the call to end.
func (n *Node) notifyNode(ctx context.Context, id, ref string, body json.RawMessage) error {
	if id == n.ID {
		go n.call(context.WithoutCancel(ctx), n.ID, ref, body)
		return nil
	}
	peer, err := n.peer(ctx, id)
	if err != nil {
		return err
	}
	return n.Network.Notify(ctx, peer, ref, body)
}

// peer returns how to reach the node id: over the session this node has with
// it, or wherever routing finds it.
func (n *Node) peer(ctx context.Context, id string) (network.Peer, error) {
	if n.Network.Connected(id) {
		return network.Peer{ID: id}, nil
	}
	return n.routing.Resolve(ctx, id)
}

// provides returns the capabilities this node announces, so other nodes can
// find it by them: those they may call, except the ones internal to a
// protocol.
func (n *Node) provides() []string {
	var refs []string
	for _, l := range n.loadedModules() {
		for _, c := range l.manifest.Capabilities {
			if c.Access == module.Network && !c.Internal {
				refs = append(refs, l.manifest.Name+"."+c.Name)
			}
		}
	}
	return refs
}

// sign signs data for a module, for a purpose it owns.
func (n *Node) sign(name, purpose string, data []byte) ([]byte, error) {
	if name != "" && !strings.HasPrefix(purpose, name+".") {
		return nil, module.Errorf(module.CodePermissionDenied, "%s may only sign for purposes starting with %q", name, name+".")
	}
	sig, err := module.Sign(n.key, purpose, data)
	if err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	return sig, nil
}

// Run runs the network, the modules' background work and their processes
// until ctx is done.
func (n *Node) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { n.Network.Run(ctx, n.routing.Peers) })
	wg.Go(func() { n.routing.Run(ctx) })
	for _, l := range n.loadedModules() {
		if r, ok := l.native.(module.Runner); ok {
			wg.Go(func() { r.Run(ctx) })
		}
		if l.process != nil {
			wg.Go(func() { l.process.supervise(ctx) })
		}
	}
	wg.Wait()
	n.close()
}

// close stops the modules' processes and closes the stores.
func (n *Node) close() {
	for _, l := range n.loadedModules() {
		if l.process != nil {
			l.process.stop()
		}
	}
	for _, s := range n.stores {
		_ = s.Close()
	}
}

// Info describes this node, see node.info.
func (n *Node) Info() module.NodeInfo {
	info := module.NodeInfo{
		ID:          n.ID,
		Name:        n.Config.Name,
		Description: n.Config.Desc,
		PublicKey:   n.key.Public().(ed25519.PublicKey),
		Addrs:       n.Network.Addrs(),
		DirectAddrs: n.Network.DirectAddrs(),
		Observed:    n.Network.Observed(),
		ListenPort:  n.Network.ListenPort(),
	}
	for _, l := range n.loadedModules() {
		m := l.manifest
		mi := module.ModuleInfo{Name: m.Name, Version: m.Version, Description: m.Description, Runtime: l.runtime}
		for _, c := range m.Capabilities {
			mi.Capabilities = append(mi.Capabilities, module.CapabilityInfo{
				Ref:         m.Name + "." + c.Name,
				Description: c.Description,
				Access:      cmp.Or(c.Access, module.Local),
				Internal:    c.Internal,
			})
		}
		for _, e := range m.Events {
			mi.Events = append(mi.Events, module.EventInfo{Ref: m.Name + "." + e.Name, Description: e.Description})
		}
		info.Modules = append(info.Modules, mi)
	}
	return info
}

// Capability returns the declaration of the capability ref, e.g. for its
// schemas, and the manifest it's in.
func (n *Node) Capability(ref string) (module.Capability, module.Manifest, bool) {
	n.mu.RLock()
	c := n.caps[ref]
	n.mu.RUnlock()
	if c == nil {
		return module.Capability{}, module.Manifest{}, false
	}
	return c.spec, c.module.manifest, true
}

// compile compiles a capability's input schema, with the manifest's defs.
func compile(m module.Manifest, schema map[string]any) (*jsonschema.Schema, error) {
	data, err := m.Schema(schema)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	const url = "mem:///schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

// validate checks body against schema, and says what's wrong in one line.
func validate(schema *jsonschema.Schema, body json.RawMessage) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := schema.Validate(v); err != nil {
		// The first line names the schema; the rest are the problems.
		lines := strings.Split(err.Error(), "\n")
		for i := range lines {
			lines[i] = strings.TrimLeft(lines[i], " -")
		}
		return fmt.Errorf("%s", strings.Join(lines[min(1, len(lines)-1):], "; "))
	}
	return nil
}
