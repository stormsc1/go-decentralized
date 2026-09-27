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
	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/modules"
)

func main() {
	configPath := flag.String("config", envOr("NODE_CONFIG", "infra/gl-organization.node.yaml"), "path to the node definition")
	listen := flag.String("listen", envOr("NODE_LISTEN", ":8080"), "address to serve the node API on")
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
	env := module.Env{
		NodeID: identity.NodeID(key),
		Key:    key,
		// Reachable addresses are found via dial-backs; NODE_ADDRESS adds one
		// that is always advertised, e.g. a public DNS name.
		Network: network.New(network.Config{
			ID:         identity.NodeID(key),
			ListenPort: port,
			Announce:   []string{os.Getenv("NODE_ADDRESS")},
			Private:    *private,
		}),
	}

	n, err := node.New(cfg, env, modules.Factories)
	if err != nil {
		fatal("build node", err)
	}

	srv := &http.Server{Addr: *listen, Handler: n.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go n.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("Node ready", "name", cfg.Name, "id", env.NodeID, "listen", *listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve", err)
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
