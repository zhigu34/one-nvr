package main

import (
	"github.com/zhigu34/one-nvr/internal/app"
	"log/slog"
	"os"
)

func main() {
	if err := app.Run("worker"); err != nil {
		slog.Error("Worker stopped", "error", err)
		os.Exit(1)
	}
}
