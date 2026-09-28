package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"go-decentralized/module"
)

const (
	startTimeout = 10 * time.Second // for a process module to start
	stopTimeout  = 5 * time.Second  // for it to exit once stdin closes
	restartDelay = 5 * time.Second  // before restarting one that exited
)

// process runs a module in a process of its own, and carries calls to and
// from it over the process's stdin and stdout. See spec/modules.md, "Process
// modules".
type process struct {
	n        *Node
	name     string
	run      []string
	config   json.RawMessage
	manifest module.Manifest // as the module first started

	mu     sync.Mutex
	link   *module.Link // nil while the module isn't running
	cmd    *exec.Cmd
	exited chan struct{}
}

// startProcess runs a process module and starts it.
func (n *Node) startProcess(name string, run []string, config json.RawMessage) (*process, error) {
	p := &process{n: n, name: name, run: run, config: config, exited: make(chan struct{})}
	manifest, err := p.start()
	if err != nil {
		return nil, err
	}
	p.manifest = manifest
	return p, nil
}

// start runs the process and starts the module in it, returning its
// manifest.
func (p *process) start() (module.Manifest, error) {
	cmd := exec.Command(p.run[0], p.run[1:]...)
	cmd.Env = append(os.Environ(), module.ProtocolEnv+"="+module.Protocol)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return module.Manifest{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return module.Manifest{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return module.Manifest{}, err
	}
	if err := cmd.Start(); err != nil {
		return module.Manifest{}, err
	}
	link := module.NewLink(context.Background(), module.NewStdioStream(stdout, stdin), p.handle)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		var logged sync.WaitGroup
		logged.Go(func() { p.log(stderr) })
		// The link closes when the module closes stdout, when it sends
		// something that isn't a message, or when stop closes stdin. The
		// module then has stopTimeout to exit.
		<-link.Done()
		waited := make(chan error, 1)
		go func() {
			logged.Wait()
			waited <- cmd.Wait()
		}()
		select {
		case err := <-waited:
			slog.Info("module: exited", "module", p.name, "status", err)
		case <-time.After(stopTimeout):
			_ = cmd.Process.Kill()
			slog.Warn("module: killed", "module", p.name, "status", <-waited)
		}
	}()
	p.mu.Lock()
	p.link, p.cmd, p.exited = link, cmd, exited
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	config := p.config
	if config == nil {
		config = json.RawMessage("{}")
	}
	body, err := json.Marshal(map[string]any{
		"node":   map[string]string{"id": p.n.ID, "name": p.n.Config.Name, "data_dir": p.n.Config.DataDir},
		"config": config,
	})
	if err != nil {
		p.stop()
		return module.Manifest{}, err
	}
	result, err := link.Call(ctx, module.Call{Ref: "module.start", Input: body})
	if err != nil {
		p.stop()
		return module.Manifest{}, fmt.Errorf("start: %w", err)
	}
	var started struct {
		Manifest module.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(result, &started); err != nil {
		p.stop()
		return module.Manifest{}, fmt.Errorf("start: %w", err)
	}
	return started.Manifest, nil
}

// handle handles a call the module makes: to another node if it says so,
// else to this node's capabilities, as the node.
func (p *process) handle(ctx context.Context, call module.Call) (json.RawMessage, error) {
	ctx = context.WithValue(ctx, moduleKey{}, p.name)
	if call.Notify {
		return nil, p.n.notifyNode(ctx, cmp.Or(call.To, p.n.ID), call.Ref, call.Input)
	}
	if call.To != "" {
		return p.n.callNode(ctx, call.To, call.Ref, call.Input)
	}
	return p.n.call(ctx, p.n.ID, call.Ref, call.Input)
}

// handlers pass calls to the module's capabilities on to its process.
func (p *process) handlers() map[string]handler {
	hs := map[string]handler{}
	for _, c := range p.manifest.Capabilities {
		ref := p.name + "." + c.Name
		hs[c.Name] = func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
			return p.call(ctx, module.Call{Ref: ref, Input: body, From: module.Caller(ctx), User: module.User(ctx)})
		}
	}
	return hs
}

func (p *process) call(ctx context.Context, call module.Call) (json.RawMessage, error) {
	p.mu.Lock()
	link := p.link
	p.mu.Unlock()
	if link == nil {
		return nil, module.Errorf(module.CodeUnavailable, "module %s isn't running", p.name)
	}
	return link.Call(ctx, call)
}

// inspect asks the module for its state.
func (p *process) inspect(ctx context.Context) (json.RawMessage, error) {
	return p.call(ctx, module.Call{Ref: "module.inspect"})
}

// supervise restarts the module whenever its process exits, until ctx is
// done, then stops it.
func (p *process) supervise(ctx context.Context) {
	for {
		p.mu.Lock()
		exited := p.exited
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			p.stop()
			return
		case <-exited:
		}
		p.mu.Lock()
		p.link = nil
		p.mu.Unlock()
		slog.Warn("module: restarting", "module", p.name, "in", restartDelay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartDelay):
		}
		manifest, err := p.start()
		if err != nil {
			slog.Warn("module: restart failed", "module", p.name, "err", err)
			continue
		}
		if !slices.EqualFunc(manifest.Capabilities, p.manifest.Capabilities, func(a, b module.Capability) bool { return a.Name == b.Name }) {
			slog.Warn("module: its capabilities changed; restart the node to serve the new ones", "module", p.name)
		}
	}
}

// stop closes the link to the module, and so its stdin, which tells it to
// exit, and waits until it has, or was killed.
func (p *process) stop() {
	p.mu.Lock()
	link, exited := p.link, p.exited
	p.link = nil
	p.mu.Unlock()
	if link != nil {
		_ = link.Close()
	}
	<-exited
}

// log logs what the module writes to stderr, line by line.
func (p *process) log(stderr io.Reader) {
	lines := bufio.NewScanner(stderr)
	for lines.Scan() {
		slog.Info(lines.Text(), "module", p.name)
	}
}
