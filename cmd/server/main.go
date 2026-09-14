package main

import (
	"context"
	"os"

	"github.com/pelfox/gophprofile/internal/app"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/telemetry"
)

func main() {
	cfg, err := config.Load()
	logger := logging.New("gophprofile-server")
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}
	logger = logging.New(cfg.OTELServiceName)

	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, cfg.OTELServiceName, cfg.OTELExporterEndpoint)
	if err != nil {
		logger.Error("failed to set up telemetry", "error", err)
		os.Exit(1)
	}

	runErr := app.Run(logger, cfg)

	if err := shutdown(context.Background()); err != nil {
		logger.Error("failed to shut down telemetry", "error", err)
	}

	if runErr != nil {
		logger.Error("failed to run the application", "error", runErr)
		os.Exit(1)
	}
}
