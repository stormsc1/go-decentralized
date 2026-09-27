package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"go-decentralized/internal/node"
	"go-decentralized/module"
)

// CLI is the interactive terminal that calls the CLI node's capabilities.
type CLI struct {
	Node    *node.Node
	Timeout time.Duration
}

// REPL reads commands from in until EOF or "exit".
func (c *CLI) REPL(in io.Reader) {
	fmt.Println(`Type "help" for commands, "exit" to quit.`)
	scanner := bufio.NewScanner(in)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "":
			continue
		case "exit", "quit":
			return
		case "help":
			c.printHelp()
		default:
			c.Run(line)
		}
	}
}

// Run calls "<module>.<capability> [key=value ...]", or "<module>.<capability>
// {json}", on the CLI node.
func (c *CLI) Run(line string) bool {
	ref, args, _ := strings.Cut(strings.TrimSpace(line), " ")
	input, err := c.input(ref, strings.TrimSpace(args))
	if err != nil {
		fmt.Println("error:", err)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	result, err := c.Node.Call(ctx, ref, input)
	if err != nil {
		fmt.Println("error:", err)
		return false
	}
	printResult(ref, result)
	return true
}

// input builds a call's input from key=value arguments, typed as the
// capability's schema says, or takes it as JSON.
func (c *CLI) input(ref, args string) (json.RawMessage, error) {
	if strings.HasPrefix(args, "{") {
		return json.RawMessage(args), nil
	}
	in := map[string]any{}
	props := c.properties(ref)
	for _, kv := range strings.Fields(args) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("argument %q must be key=value", kv)
		}
		in[k] = v
		switch props[k]["type"] {
		case "integer", "number", "boolean", "array", "object":
			var typed any
			if json.Unmarshal([]byte(v), &typed) == nil {
				in[k] = typed
			}
		}
	}
	return json.Marshal(in)
}

// properties returns the schemas of the properties of a capability's input.
func (c *CLI) properties(ref string) map[string]map[string]any {
	spec, manifest, ok := c.Node.Capability(ref)
	if !ok {
		return nil
	}
	props := map[string]map[string]any{}
	all, _ := resolve(manifest, spec.Input)["properties"].(map[string]any)
	for name, s := range all {
		if s, ok := s.(map[string]any); ok {
			props[name] = resolve(manifest, s)
		}
	}
	return props
}

// resolve follows a schema's reference to the manifest's defs, if it has one.
func resolve(m module.Manifest, s map[string]any) map[string]any {
	ref, _ := s["$ref"].(string)
	if name, ok := strings.CutPrefix(ref, "#/$defs/"); ok {
		if def, ok := m.Defs[name].(map[string]any); ok {
			return resolve(m, def)
		}
	}
	return s
}

type contact struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

type report struct {
	contact
	Direct  bool                       `json:"direct"`
	Modules map[string]json.RawMessage `json:"modules"`
	Error   string                     `json:"error"`
}

type trace struct {
	Time     time.Time     `json:"time"`
	Ref      string        `json:"ref"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Addr     string        `json:"addr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error"`
}

// printResult prints tables for the results people read most, and JSON for
// the rest.
func printResult(ref string, result json.RawMessage) {
	var out struct {
		Nodes     json.RawMessage `json:"nodes"`
		Providers []contact       `json:"providers"`
		Traces    []trace         `json:"traces"`
	}
	_ = json.Unmarshal(result, &out)
	switch ref {
	case "discovery.list_nodes":
		var nodes []contact
		_ = json.Unmarshal(out.Nodes, &nodes)
		printContacts(nodes)
	case "discovery.find_capability_providers":
		printContacts(out.Providers)
	case "discovery.find_node_by_id":
		var c contact
		_ = json.Unmarshal(result, &c)
		printContacts([]contact{c})
	case "debug.map_network":
		var reports []report
		_ = json.Unmarshal(out.Nodes, &reports)
		printReports(reports)
	case "debug.traffic":
		printTraces(out.Traces)
	default:
		var pretty bytes.Buffer
		if json.Indent(&pretty, result, "", "  ") != nil {
			pretty.Write(result)
		}
		fmt.Println(pretty.String())
	}
}

func printContacts(nodes []contact) {
	if len(nodes) == 0 {
		fmt.Println("no nodes found")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tADDRESS")
	for _, c := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, c.ID, strings.Join(c.Addrs, ","))
	}
	tw.Flush()
	fmt.Printf("%d node(s)\n", len(nodes))
}

func printReports(reports []report) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tMODE\tMODULES\tADDRESS")
	for _, n := range reports {
		mode := "relayed"
		switch {
		case n.Error != "":
			mode = "unreachable"
		case n.Direct:
			mode = "direct"
		}
		modules := slices.Sorted(maps.Keys(n.Modules))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.ID, mode, strings.Join(modules, ","), strings.Join(n.Addrs, ","))
	}
	tw.Flush()
	fmt.Printf("%d node(s)\n", len(reports))
}

func printTraces(traces []trace) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tCALL\tFROM\tTO\tADDRESS\tTOOK\tERROR")
	for _, t := range traces {
		fmt.Fprintf(tw, "%s\t%s\t%.8s\t%.8s\t%s\t%s\t%s\n",
			t.Time.Format("15:04:05.000"), t.Ref, t.From, t.To, t.Addr, t.Duration.Round(time.Millisecond), t.Error)
	}
	tw.Flush()
}

// printHelp lists the capabilities users call, with their arguments, from
// the modules' manifests.
func (c *CLI) printHelp() {
	fmt.Println("Usage: <module>.<capability> [key=value ...], or <module>.<capability> {json}")
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, m := range c.Node.Info().Modules {
		for _, cp := range m.Capabilities {
			if cp.Internal {
				continue
			}
			var args []string
			props := c.properties(cp.Ref)
			for _, name := range slices.Sorted(maps.Keys(props)) {
				typ, _ := props[name]["type"].(string)
				args = append(args, fmt.Sprintf("%s=<%s>", name, cmp.Or(typ, "value")))
			}
			fmt.Fprintf(tw, "  %s %s\t%s\n", cp.Ref, strings.Join(args, " "), cp.Description)
		}
	}
	tw.Flush()
	fmt.Println()
	fmt.Println("  help\tshow this help")
	fmt.Println("  exit\tquit")
}
