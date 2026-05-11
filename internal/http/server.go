package http

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/ravhn/echoapp-backend/internal/config"
)

type Server struct {
	e   *echo.Echo
	log *slog.Logger
}

func New(cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(slogRequestLogger(log))

	registerHealth(e, pool, rdb)

	return &Server{e: e, log: log}
}

func (s *Server) Start(addr string) error {
	return s.e.Start(addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.e.Shutdown(ctx)
}
