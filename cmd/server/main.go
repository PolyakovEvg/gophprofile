package cmd

import (
	"os"

	"github.com/pelfox/gophprofile/internal/app"
	"github.com/pelfox/gophprofile/internal/config"
	"github.com/rs/zerolog"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load configuration")
	}

	if err := app.Run(logger, cfg); err != nil {
		logger.Fatal().Err(err).Msg("failed to run the application")
	}
}
