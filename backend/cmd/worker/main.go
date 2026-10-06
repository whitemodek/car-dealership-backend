package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/whitemodek/car-dealership-backend/backend/internal/database"
	"github.com/whitemodek/car-dealership-backend/backend/internal/platform"
	"github.com/whitemodek/car-dealership-backend/backend/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("worker stopped")
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	config, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, config.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return (worker.Worker{Pool: pool, Logger: logger, WebhookURL: config.WebhookURL, Secret: config.WebhookSecret}).Run(ctx)
}
