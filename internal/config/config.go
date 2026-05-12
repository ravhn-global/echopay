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

	// Cooldown applied when a user raises any sending limit. Lowering is
	// always instant. Default matches the design system; shorten in dev
	// to test the pending-change flow.
	RiskLimitRaiseCooldown time.Duration `env:"RISK_LIMIT_RAISE_COOLDOWN" envDefault:"24h"`

	// Clients below MinSupportedAppVersion are blocked at startup. Only
	// bump for security-critical releases; routine releases use the soft
	// banner update path (not gated server-side).
	MinSupportedAppVersion string `env:"MIN_SUPPORTED_APP_VERSION" envDefault:"0.1.0"`
	LatestAppVersion       string `env:"LATEST_APP_VERSION" envDefault:"0.1.0"`

	// KYC provider — "stub" (default; format checks only) or "youverify".
	// Switch to "youverify" once you have a sandbox key from
	// https://app.youverify.co. Production base URL is the no-sandbox host.
	KYCProvider       string `env:"KYC_PROVIDER" envDefault:"stub"`
	YouVerifyBaseURL  string `env:"YOUVERIFY_BASE_URL" envDefault:"https://api.sandbox.youverify.co"`
	YouVerifyAPIKey   string `env:"YOUVERIFY_API_KEY"`
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
