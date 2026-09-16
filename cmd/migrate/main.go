// Command migrate applies the embedded database schema migrations and
// exits. It backs the Helm pre-install/pre-upgrade migration hook Job so
// schema changes land before new server/worker pods roll out; it shares the
// application's DATABASE_URL configuration and embedded migration files.
package main

import (
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/migrate"
)

type migrateConfig struct {
	DatabaseURL string `env:"DATABASE_URL" env-required:"true"`
}

const (
	// connectRetries and connectRetryDelay bound how long this binary waits
	// for the database to accept connections. This matters most as a
	// Kubernetes Helm hook Job, which may start racing a freshly-created
	// database pod that is not accepting connections yet.
	connectRetries    = 20
	connectRetryDelay = 3 * time.Second
)

func main() {
	logger := logging.New("gophprofile-migrate")

	_ = godotenv.Load()
	var cfg migrateConfig
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		logger.Error("failed to read environment", "error", err)
		os.Exit(1)
	}

	var err error
	for attempt := 1; attempt <= connectRetries; attempt++ {
		if err = migrate.Up(cfg.DatabaseURL); err == nil {
			logger.Info("database migrations applied")
			return
		}

		logger.Warn("migration attempt failed, retrying",
			"attempt", attempt,
			"max_attempts", connectRetries,
			"error", err,
		)
		time.Sleep(connectRetryDelay)
	}

	logger.Error("failed to apply database migrations", "error", err)
	os.Exit(1)
}
