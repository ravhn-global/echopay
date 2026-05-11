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
	"github.com/ravhn/echoapp-backend/internal/logger"
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

	srv := httpsrv.New(cfg, log, pool, rdb)

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
