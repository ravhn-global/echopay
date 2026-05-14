package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/ravhn/echoapp-backend/internal/audit"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/push"
	"github.com/ravhn/echoapp-backend/internal/store"
)

var (
	ErrOTPNotFound    = errors.New("no active OTP request for this phone")
	ErrOTPExpired     = errors.New("OTP expired")
	ErrOTPMaxAttempts = errors.New("too many attempts")
	ErrOTPInvalid     = errors.New("invalid OTP")
)

const (
	otpTTL         = 5 * time.Minute
	otpMaxAttempts = 5
)

type Service struct {
	q       store.Querier
	issuer  *Issuer
	push    *push.Service
	audit   *audit.Service
	log     *slog.Logger
	devMode bool
}

func NewService(q store.Querier, issuer *Issuer, pushSvc *push.Service, auditSvc *audit.Service, log *slog.Logger, devMode bool) *Service {
	return &Service{q: q, issuer: issuer, push: pushSvc, audit: auditSvc, log: log, devMode: devMode}
}

// QA fixture: when devMode is on, these two phones accept the magic OTP
// "000000" without going through the bcrypt hash check. Lets the team
// click through pay/receive flows on both sides without fishing the
// real code out of the response on every fresh login. The bypass is
// dead code in production (devMode = ENV != production).
var magicOTPPhones = map[string]struct{}{
	"+2348012345678": {}, // Test User
	"+2348022222222": {}, // Adaeze Okeke (counterparty)
}

const magicOTPCode = "000000"

func isMagicOTPPhone(phone string) bool {
	_, ok := magicOTPPhones[phone]
	return ok
}

// RequestOTP generates a fresh OTP, expires any pending ones for the same
// phone, and returns the OTP. In dev/local env the caller logs it for testing;
// in prod it would be sent to an SMS provider.
type OTPResult struct {
	Code      string
	ExpiresAt time.Time
}

func (s *Service) RequestOTP(ctx context.Context, phone, purpose string) (*OTPResult, error) {
	if purpose == "" {
		purpose = "signin"
	}
	if err := s.q.ExpirePreviousOTPs(ctx, store.ExpirePreviousOTPsParams{
		Phone:   phone,
		Purpose: purpose,
	}); err != nil {
		return nil, fmt.Errorf("expire previous otps: %w", err)
	}

	code, err := GenerateOTP()
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash otp: %w", err)
	}
	expires := time.Now().Add(otpTTL)
	if _, err := s.q.CreateOTPRequest(ctx, store.CreateOTPRequestParams{
		Phone:     phone,
		CodeHash:  string(hash),
		Purpose:   purpose,
		ExpiresAt: pgconv.TimeFrom(expires),
	}); err != nil {
		return nil, fmt.Errorf("create otp: %w", err)
	}
	return &OTPResult{Code: code, ExpiresAt: expires}, nil
}

// DeviceInfo carries identifying info from the client at signin. Every
// field is optional — clients on platforms that don't expose a value
// just send empty strings.
type DeviceInfo struct {
	Fingerprint string
	Model       string
	OSName      string
	OSVersion   string
	AppVersion  string
}

type VerifyResult struct {
	Token   string
	User    store.User
	IsNew   bool
	Session store.DeviceSession
}

// VerifyOTP checks the OTP, upserts a user, creates a device_session row,
// and returns a JWT whose jti claim is that session id.
func (s *Service) VerifyOTP(ctx context.Context, phone, purpose, code string, device DeviceInfo) (*VerifyResult, error) {
	if purpose == "" {
		purpose = "signin"
	}
	req, err := s.q.GetLatestOTPRequest(ctx, store.GetLatestOTPRequestParams{
		Phone:   phone,
		Purpose: purpose,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOTPNotFound
		}
		return nil, fmt.Errorf("get otp: %w", err)
	}
	if pgconv.TimeTo(req.ExpiresAt).Before(time.Now()) {
		return nil, ErrOTPExpired
	}
	if req.Attempts >= req.MaxAttempts {
		return nil, ErrOTPMaxAttempts
	}
	if _, err := s.q.IncrementOTPAttempts(ctx, req.ID); err != nil {
		return nil, fmt.Errorf("increment attempts: %w", err)
	}
	// Magic-code fast path for QA: skip the hash check for whitelisted
	// test phones in dev mode. Everything else still goes through bcrypt.
	if !(s.devMode && code == magicOTPCode && isMagicOTPPhone(phone)) {
		if err := bcrypt.CompareHashAndPassword([]byte(req.CodeHash), []byte(code)); err != nil {
			return nil, ErrOTPInvalid
		}
	}
	if err := s.q.MarkOTPVerified(ctx, req.ID); err != nil {
		return nil, fmt.Errorf("mark otp verified: %w", err)
	}

	user, isNew, err := s.upsertUser(ctx, phone)
	if err != nil {
		return nil, err
	}

	token, jti, err := s.issuer.Issue(pgconv.UUIDTo(user.ID), user.Phone)
	if err != nil {
		return nil, fmt.Errorf("issue jwt: %w", err)
	}

	session, err := s.q.CreateDeviceSession(ctx, store.CreateDeviceSessionParams{
		UserID:      user.ID,
		Jti:         pgconv.UUIDFrom(jti),
		Fingerprint: nilIfEmpty(device.Fingerprint),
		Model:       nilIfEmpty(device.Model),
		OsName:      nilIfEmpty(device.OSName),
		OsVersion:   nilIfEmpty(device.OSVersion),
		AppVersion:  nilIfEmpty(device.AppVersion),
	})
	if err != nil {
		return nil, fmt.Errorf("create device session: %w", err)
	}

	// Suspicious-login push to every OTHER session — design's "New device
	// signed in" alert. Skipped for brand-new users (no other devices yet).
	if !isNew {
		deviceLabel := device.Model
		if deviceLabel == "" {
			deviceLabel = "an unknown device"
		}
		s.push.NotifyNewDeviceLogin(ctx, pgconv.UUIDTo(user.ID), pgconv.UUIDTo(session.ID), deviceLabel)
	}

	s.audit.Record(ctx, audit.Event{
		UserID:    pgconv.UUIDTo(user.ID),
		ActorID:   pgconv.UUIDTo(user.ID),
		Action:    audit.ActionAuthSignin,
		TargetTyp: "device_session",
		TargetID:  pgconv.UUIDTo(session.ID).String(),
		Metadata: map[string]any{
			"is_new_user": isNew,
			"device":      device.Model,
			"os":          device.OSName,
		},
	})

	return &VerifyResult{Token: token, User: user, IsNew: isNew, Session: session}, nil
}

func (s *Service) upsertUser(ctx context.Context, phone string) (store.User, bool, error) {
	user, err := s.q.GetUserByPhone(ctx, phone)
	if err == nil {
		return user, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.User{}, false, fmt.Errorf("get user by phone: %w", err)
	}
	user, err = s.q.CreateUser(ctx, phone)
	if err != nil {
		return store.User{}, false, fmt.Errorf("create user: %w", err)
	}
	return user, true, nil
}

// CurrentUser fetches a user by ID (used by /v1/me).
func (s *Service) CurrentUser(ctx context.Context, userID uuid.UUID) (store.User, error) {
	return s.q.GetUserByID(ctx, pgconv.UUIDFrom(userID))
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
