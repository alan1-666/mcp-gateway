package main

import (
	"github.com/alan1-666/mcp-gateway/internal/platform"
	"log/slog"
	"os"
)

func main() {
	if err := platform.Run("api-server"); err != nil {
		slog.Error("api-server stopped", "error", err)
		os.Exit(1)
	}
}
