package http

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/ravhn/echoapp-backend/internal/auth"
	"github.com/ravhn/echoapp-backend/internal/config"
	"github.com/ravhn/echoapp-backend/internal/kyc"
	"github.com/ravhn/echoapp-backend/internal/ledger"
	"github.com/ravhn/echoapp-backend/internal/mandates"
	"github.com/ravhn/echoapp-backend/internal/payments"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/store"
	"github.com/ravhn/echoapp-backend/internal/tokens"
)

type Server struct {
	e           *echo.Echo
	log         *slog.Logger
	PaymentsSvc *payments.Service // exposed for the reconciler in main
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

	// Wire dependencies.
	queries := store.New(pool)
	ps := paystack.New(cfg.PaystackSecretKey)
	jwtIssuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTTTL)

	authSvc := auth.NewService(queries, jwtIssuer, log)
	kycSvc := kyc.NewService(queries)
	mandatesSvc := mandates.NewService(queries, ps)
	tokensSvc := tokens.NewService(queries, rdb)
	ledgerSvc := ledger.NewService(queries)
	paymentsSvc := payments.NewService(queries, tokensSvc, ledgerSvc, ps, log)

	// Webhooks (public, signature-verified).
	(&webhookHandler{
		paymentsSvc: paymentsSvc,
		webhookKey:  cfg.PaystackWebhookKey,
		devMode:     cfg.IsDev(),
		log:         log,
	}).mount(e)

	// Public (unauthenticated) routes.
	v1Public := e.Group("/v1")
	auth.NewHandler(authSvc, log, cfg.IsDev()).Mount(v1Public)

	// Authenticated routes.
	v1Auth := e.Group("/v1", AuthMiddleware(jwtIssuer))
	(&meHandler{authSvc: authSvc, q: queries, ps: ps}).mount(v1Auth)
	kyc.NewHandler(kycSvc, UserIDFrom).Mount(v1Auth)
	mandates.NewHandler(mandatesSvc, UserIDFrom).Mount(v1Auth)
	tokens.NewHandler(tokensSvc, UserIDFrom).Mount(v1Auth)
	payments.NewHandler(paymentsSvc, UserIDFrom).Mount(v1Auth)

	return &Server{e: e, log: log, PaymentsSvc: paymentsSvc}
}

func (s *Server) Start(addr string) error {
	return s.e.Start(addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.e.Shutdown(ctx)
}
