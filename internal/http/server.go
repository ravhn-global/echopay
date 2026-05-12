package http

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/ravhn/echoapp-backend/internal/audit"
	"github.com/ravhn/echoapp-backend/internal/auth"
	"github.com/ravhn/echoapp-backend/internal/config"
	"github.com/ravhn/echoapp-backend/internal/kyc"
	"github.com/ravhn/echoapp-backend/internal/ledger"
	"github.com/ravhn/echoapp-backend/internal/mandates"
	"github.com/ravhn/echoapp-backend/internal/payments"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/push"
	"github.com/ravhn/echoapp-backend/internal/risk"
	"github.com/ravhn/echoapp-backend/internal/store"
	"github.com/ravhn/echoapp-backend/internal/tokens"
)

type Server struct {
	e           *echo.Echo
	log         *slog.Logger
	PaymentsSvc *payments.Service     // exposed for the reconciler in main
	LimitsSvc   *risk.LimitsService   // exposed so main can apply due pending limits
}

func New(cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client, pushSvc *push.Service) *Server {
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

	auditSvc := audit.NewService(queries, log)
	authSvc := auth.NewService(queries, jwtIssuer, pushSvc, auditSvc, log)

	var kycProvider kyc.Provider = kyc.StubProvider{}
	if cfg.KYCProvider == "youverify" && cfg.YouVerifyAPIKey != "" {
		kycProvider = kyc.NewYouVerifyProvider(
			kyc.NewYouVerifyClient(cfg.YouVerifyBaseURL, cfg.YouVerifyAPIKey),
		)
		log.Info("kyc provider", "name", "youverify")
	} else {
		log.Info("kyc provider", "name", "stub")
	}
	kycSvc := kyc.NewService(queries, kycProvider, log)
	mandatesSvc := mandates.NewService(queries, ps)
	tokensSvc := tokens.NewService(queries, rdb)
	ledgerSvc := ledger.NewService(queries)
	trustedSvc := risk.NewTrustedService(queries)
	limitsSvc := risk.NewLimitsService(queries, cfg.RiskLimitRaiseCooldown, log)
	paymentsSvc := payments.NewService(queries, tokensSvc, ledgerSvc, trustedSvc, ps, pushSvc, auditSvc, log)

	// Webhooks (public, signature-verified).
	(&webhookHandler{
		paymentsSvc: paymentsSvc,
		webhookKey:  cfg.PaystackWebhookKey,
		devMode:     cfg.IsDev(),
		log:         log,
	}).mount(e)

	// App-version probe — public, called before signin so clients can
	// route to the force-update screen when they're below the floor.
	(&appVersionHandler{cfg: cfg}).mount(e)

	// Public (unauthenticated) routes — IP-bucketed so a buggy client
	// or scripted attacker can't burn OTPs / signin attempts.
	otpLimit := NewRateLimit(rdb, "public", 6, time.Minute)
	v1Public := e.Group("/v1", otpLimit.Middleware(IPSubject))
	auth.NewHandler(authSvc, log, cfg.IsDev()).Mount(v1Public)

	// Authenticated routes — middleware gates JWT revocation by checking
	// the jti against device_sessions, plus a user-bucketed rate limit
	// on top so a runaway client can't hammer the orchestrator.
	apiLimit := NewRateLimit(rdb, "api", 120, time.Minute)
	v1Auth := e.Group("/v1",
		AuthMiddleware(jwtIssuer, queries),
		apiLimit.Middleware(UserIDSubject),
	)
	(&meHandler{authSvc: authSvc, q: queries, ps: ps, limits: limitsSvc, push: pushSvc, audit: auditSvc}).mount(v1Auth)
	kyc.NewHandler(kycSvc, UserIDFrom).Mount(v1Auth)
	mandates.NewHandler(mandatesSvc, UserIDFrom).Mount(v1Auth)
	tokens.NewHandler(tokensSvc, UserIDFrom).Mount(v1Auth)
	payments.NewHandler(paymentsSvc, UserIDFrom).Mount(v1Auth)
	risk.NewHandler(limitsSvc, trustedSvc, UserIDFrom).Mount(v1Auth)
	(&devicesHandler{q: queries}).mount(v1Auth)
	push.NewHandler(queries, UserIDFrom, JTIFrom).Mount(v1Auth)
	audit.NewHandler(auditSvc, UserIDFrom).Mount(v1Auth)

	return &Server{e: e, log: log, PaymentsSvc: paymentsSvc, LimitsSvc: limitsSvc}
}

func (s *Server) Start(addr string) error {
	return s.e.Start(addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.e.Shutdown(ctx)
}
