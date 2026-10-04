package main

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/gatewaycontrol"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime := &gatewaycontrol.Nginx{Directory: "/control", VersionsDirectory: "/certs/versions", PublicURL: os.Getenv("ONE_NVR_PUBLIC_URL"), ProxyToken: os.Getenv("ONE_NVR_GATEWAY_PROXY_TOKEN")}
	if err := gatewaycontrol.Supervise(ctx, runtime); err != nil {
		slog.Error("gateway startup or supervision failed")
		os.Exit(1)
	}
}
