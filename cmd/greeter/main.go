// Command greeter runs the greeter module in a process of its own. Nodes
// start it for a module definition with `run: [greeter]`.
package main

import (
	"log/slog"
	"os"

	"go-decentralized/module"
	"go-decentralized/modules/greeter"
)

func main() {
	if err := module.Serve(greeter.New); err != nil {
		slog.Error("greeter", "err", err)
		os.Exit(1)
	}
}
