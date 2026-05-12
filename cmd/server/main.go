package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ravhn/echoapp-backend/internal/cache"
	"github.com/ravhn/echoapp-backend/internal/config"
	"github.com/ravhn/echoapp-backend/internal/db"
	httpsrv "github.com/ravhn/echoapp-backend/internal/http"
	"github.com/ravhn/echoapp-backend/internal/jobs"
	"github.com/ravhn/echoapp-backend/internal/logger"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/push"
	"github.com/ravhn/echoapp-backend/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Env, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	log.Info("postgres connected")

	rdb, err := cache.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Error("redis connect failed", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()
	log.Info("redis connected")

	pushSvc, err := push.NewService(
		ctx, store.New(pool),
		cfg.FCMProjectID, cfg.FCMCredentialsFile, cfg.FCMCredentialsJSON,
		log,
	)
	if err != nil {
		log.Error("push service init failed", "err", err)
		os.Exit(1)
	}

	srv := httpsrv.New(cfg, log, pool, rdb, pushSvc)

	// Background reconciler — re-uses the same Querier and Paystack client
	// the HTTP server uses. Idempotent operations keep it safe to run
	// concurrent with the HTTP path. Also applies due limit-raise changes.
	reconciler := jobs.NewReconciler(
		store.New(pool),
		paystack.New(cfg.PaystackSecretKey),
		srv.LimitsSvc,
		log,
	)
	go reconciler.Run(ctx)

	go func() {
		log.Info("server starting", "addr", cfg.HTTPAddr)
		if err := srv.Start(cfg.HTTPAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server crashed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown error", "err", err)
		os.Exit(1)
	}
	log.Info("server stopped cleanly")
}
