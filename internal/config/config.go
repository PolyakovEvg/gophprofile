package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

// AppConfig stores application settings loaded from the environment.
type AppConfig struct {
	// ListenAddr is the address the HTTP server listens on.
	ListenAddr string `env:"LISTEN_ADDR" env-required:"true"`
	// RabbitMQURL is the connection URL for RabbitMQ.
	RabbitMQURL string `env:"RABBITMQ_URL" env-required:"true"`
	// DatabaseURL is the database connection URL.
	DatabaseURL string `env:"DATABASE_URL" env-required:"true"`

	// S3Region is the AWS S3 region used for object storage.
	S3Region string `env:"S3_REGION" env-required:"true"`
	// S3Endpoint is the custom S3-compatible endpoint, if one is used.
	S3Endpoint string `env:"S3_ENDPOINT"`
	// S3AccessKey is the access key for S3-compatible storage.
	S3AccessKey string `env:"S3_ACCESS_KEY" env-required:"true"`
	// S3SecretKey is the secret key for S3-compatible storage.
	S3SecretKey string `env:"S3_SECRET_KEY" env-required:"true"`
	// S3Bucket is the bucket used for avatar object storage.
	S3Bucket string `env:"S3_BUCKET" env-required:"true"`
}

// Load loads AppConfig from .env and environment variables.
func Load() (*AppConfig, error) {
	_ = godotenv.Load()

	var cfg AppConfig
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to read environment: %w", err)
	}

	return &cfg, nil
}

type WorkerConfig struct {
	// RabbitMQURL is the connection URL for RabbitMQ.
	RabbitMQURL string `env:"RABBITMQ_URL" env-required:"true"`
	// DatabaseURL is the database connection URL.
	DatabaseURL string `env:"DATABASE_URL" env-required:"true"`

	// S3Region is the AWS S3 region used for object storage.
	S3Region string `env:"S3_REGION" env-required:"true"`
	// S3Endpoint is the custom S3-compatible endpoint, if one is used.
	S3Endpoint string `env:"S3_ENDPOINT"`
	// S3AccessKey is the access key for S3-compatible storage.
	S3AccessKey string `env:"S3_ACCESS_KEY" env-required:"true"`
	// S3SecretKey is the secret key for S3-compatible storage.
	S3SecretKey string `env:"S3_SECRET_KEY" env-required:"true"`
	// S3Bucket is the bucket used for avatar object storage.
	S3Bucket string `env:"S3_BUCKET" env-required:"true"`
}

func LoadWorkerConfig() (*WorkerConfig, error) {
	_ = godotenv.Load()

	var cfg WorkerConfig
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to read environment: %w", err)
	}

	return &cfg, nil
}
