package main

import (
	"github.com/alan1-666/mcp-gateway/internal/platform"
	"log/slog"
	"os"
)

func main() {
	if err := platform.Run("gateway"); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
