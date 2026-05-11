package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	Env         string `env:"ENV" envDefault:"local"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
	HTTPAddr    string `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL string `env:"DATABASE_URL,required"`
	RedisURL    string `env:"REDIS_URL,required"`

	JWTSecret string        `env:"JWT_SECRET,required"`
	JWTTTL    time.Duration `env:"JWT_TTL" envDefault:"720h"` // 30 days

	PaystackSecretKey  string `env:"PAYSTACK_SECRET_KEY,required"`
	PaystackPublicKey  string `env:"PAYSTACK_PUBLIC_KEY"`
	PaystackWebhookKey string `env:"PAYSTACK_WEBHOOK_KEY"`
}

func (c *Config) IsDev() bool {
	return c.Env == "local" || c.Env == "dev"
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	return cfg, nil
}
