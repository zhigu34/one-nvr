package main

import (
	"github.com/zhigu34/one-nvr/internal/app"
	"log/slog"
	"os"
)

func main() {
	if err := app.Run("api"); err != nil {
		slog.Error("API stopped", "error", err)
		os.Exit(1)
	}
}
