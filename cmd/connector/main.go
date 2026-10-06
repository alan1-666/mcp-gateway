// Connector uses an outbound HTTPS connection and locally approved targets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alan1-666/mcp-gateway/internal/connectoragent"
)

func main() {
	path := flag.String("config", "", "absolute path to local Connector JSON configuration")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: connector --config /path/to/config.json")
		os.Exit(2)
	}
	config, err := connectoragent.LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	agent, err := connectoragent.New(ctx, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	err = agent.Run(ctx)
	agent.Close()
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
