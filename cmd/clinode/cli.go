package main

import (
	"bufio"
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

	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/modules/debug"
	"go-decentralized/modules/discovery/kademlia"
)

// CLI is the interactive terminal that triggers the CLI node's capabilities.
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

// Run parses "<module>.<capability> [key=value ...]" and invokes it locally.
func (c *CLI) Run(line string) bool {
	fields := strings.Fields(line)
	ref, args := fields[0], module.Args{}
	for _, kv := range fields[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Printf("error: argument %q must be key=value\n", kv)
			return false
		}
		args[k] = v
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	result, err := c.Node.Registry.Invoke(ctx, ref, args)
	if err != nil {
		fmt.Println("error:", err)
		return false
	}
	printResult(result)
	return true
}

func printResult(result any) {
	switch r := result.(type) {
	case kademlia.Contact:
		printResult([]kademlia.Contact{r})
	case []kademlia.Contact:
		if len(r) == 0 {
			fmt.Println("no nodes found")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tADDRESS")
		for _, c := range r {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, c.ID, strings.Join(c.Addrs, ","))
		}
		tw.Flush()
		fmt.Printf("%d node(s)\n", len(r))
	case []network.Trace:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TIME\tMESSAGE\tFROM\tTO\tADDRESS\tTOOK\tERROR")
		for _, t := range r {
			fmt.Fprintf(tw, "%s\t%s\t%.8s\t%.8s\t%s\t%s\t%s\n",
				t.Time.Format("15:04:05.000"), t.Name, t.From, t.To, t.Addr, t.Duration.Round(time.Millisecond), t.Error)
		}
		tw.Flush()
	case []debug.Report:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tMODE\tMODULES\tADDRESS")
		for _, n := range r {
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
		fmt.Printf("%d node(s)\n", len(r))
	default:
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	}
}

func (c *CLI) printHelp() {
	fmt.Println("Usage: <module>.<capability> [key=value ...]")
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, m := range c.Node.Registry.Modules() {
		for _, cp := range m.Capabilities() {
			fmt.Fprintf(tw, "  %s.%s\t%s\n", m.Name(), cp.Name(), cp.Description())
		}
	}
	tw.Flush()
	fmt.Println()
	fmt.Println("  help\tshow this help")
	fmt.Println("  exit\tquit")
}
