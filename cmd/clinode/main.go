package main

import (
	_ "embed"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	"go-decentralized/internal/identity"
	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/modules"
)

// defaultConfig is the CLI node definition built into the binary.
//
//go:embed cli.node.yaml
var defaultConfig []byte

func main() {
	configPath := flag.String("config", "", "path to a CLI node definition (defaults to the built-in cli.node.yaml)")
	timeout := flag.Duration("timeout", 10*time.Second, "timeout per capability invocation")
	flag.Parse()

	slog.Info("Starting cli node")
	slog.Info("The CLI node is an interactive cli for interacting with the network")

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	key, err := identity.Load("")
	if err != nil {
		slog.Error("create key", "err", err)
		os.Exit(1)
	}
	// The CLI accepts no connections, so it runs in client mode.
	env := module.Env{NodeID: identity.NodeID(key), Key: key, Network: network.New(network.Config{ID: identity.NodeID(key)})}
	n, err := node.New(cfg, env, modules.Factories)
	if err != nil {
		slog.Error("build node", "err", err)
		os.Exit(1)
	}

	c := &CLI{Node: n, Timeout: *timeout}

	// A single command can be passed as arguments for scripting:
	//   clinode discovery.discover_nodes recursive=false
	if flag.NArg() > 0 {
		if !c.Run(strings.Join(flag.Args(), " ")) {
			os.Exit(1)
		}
		return
	}
	c.REPL(os.Stdin)
}

func loadConfig(path string) (node.Config, error) {
	if path != "" {
		return node.LoadConfig(path)
	}
	return node.ParseConfig("cli.node.yaml", defaultConfig)
}
