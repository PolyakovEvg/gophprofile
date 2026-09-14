package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/telemetry"
	"github.com/pelfox/gophprofile/internal/worker"
)

func main() {
	logger := logging.New("gophprofile-worker")

	cfg, err := config.LoadWorkerConfig()
	if err != nil {
		logger.Error("failed to load worker configuration", "error", err)
		os.Exit(1)
	}
	logger = logging.New(cfg.OTELServiceName)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	shutdown, err := telemetry.Setup(ctx, cfg.OTELServiceName, cfg.OTELExporterEndpoint)
	if err != nil {
		logger.Error("failed to set up telemetry", "error", err)
		os.Exit(1)
	}

	runErr := worker.Run(ctx, logger, cfg)

	if err := shutdown(context.Background()); err != nil {
		logger.Error("failed to shut down telemetry", "error", err)
	}

	if runErr != nil {
		logger.Error("failed to run the worker", "error", runErr)
		os.Exit(1)
	}
}
