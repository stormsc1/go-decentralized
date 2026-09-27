package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/modules"
)

func main() {
	configPath := flag.String("config", envOr("NODE_CONFIG", "infra/gl-organization.node.yaml"), "path to the node definition")
	listen := flag.String("listen", envOr("NODE_LISTEN", ":8080"), "address other nodes connect to, over TLS")
	apiAddr := flag.String("api", envOr("NODE_API", ""), "address to serve the local HTTP API on, e.g. for the network explorer (default: none)")
	keyPath := flag.String("key", envOr("NODE_KEY", ""), "path to the node key, created if missing (default: a new key per run)")
	private := flag.Bool("private", os.Getenv("NODE_PRIVATE") == "true", "count private and loopback addresses as reachable, for networks without public addresses (e.g. local development)")
	flag.Parse()

	slog.Info("Starting modular node", "config", *configPath)

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	key, err := identity.Load(*keyPath)
	if err != nil {
		fatal("load key", err)
	}
	_, portStr, err := net.SplitHostPort(*listen)
	if err != nil {
		fatal("parse listen address", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		fatal("parse listen port", err)
	}

	nw, err := network.New(network.Config{
		Key:        key,
		ListenPort: port,
		// Reachable addresses are found via dial-backs; NODE_ADDRESS adds one
		// that is always advertised, e.g. a public DNS name.
		Announce: []string{os.Getenv("NODE_ADDRESS")},
		Private:  *private,
		Relay:    cfg.Network.Relay,
	})
	if err != nil {
		fatal("start network", err)
	}
	n, err := node.New(cfg, key, nw, modules.Factories)
	if err != nil {
		fatal("build node", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go n.Run(ctx)
	if *apiAddr != "" {
		go serveAPI(ctx, *apiAddr, n.Handler())
	}

	slog.Info("Node ready", "name", cfg.Name, "id", n.Env.NodeID, "listen", *listen, "api", *apiAddr)
	if err := nw.ListenAndServe(ctx, *listen); err != nil {
		fatal("listen", err)
	}
}

// serveAPI serves the node's local HTTP API on addr until ctx is done.
func serveAPI(ctx context.Context, addr string, h http.Handler) {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve api", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
