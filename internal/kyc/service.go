// Package kyc handles identity verification.
//
// Two providers, switched by the KYC_PROVIDER env:
//   - "stub" (default): BVN format check + DOB sanity only. Useful for local
//     dev without burning sandbox calls.
//   - "youverify": real BVN lookup via YouVerify, with name/DOB cross-check
//     against what the user typed.
package kyc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

var (
	ErrInvalidBVN     = errors.New("BVN must be 11 digits")
	ErrInvalidDOB     = errors.New("invalid date of birth")
	ErrBVNMismatch    = errors.New("BVN doesn't match the supplied name or DOB")
	ErrProviderFailed = errors.New("identity provider failed")
)

var bvnRe = regexp.MustCompile(`^\d{11}$`)

// Provider is the strategy interface — implementations live in this package
// (stubProvider, youVerifyProvider). Anyone wanting to add a different
// upstream (e.g. Dojah, Smile Identity) implements this.
type Provider interface {
	Verify(ctx context.Context, bvn string, fullName string, dob time.Time) error
}

type Service struct {
	q   store.Querier
	p   Provider
	log *slog.Logger
}

func NewService(q store.Querier, p Provider, log *slog.Logger) *Service {
	return &Service{q: q, p: p, log: log}
}

type SubmitBVNRequest struct {
	UserID      uuid.UUID
	FullName    string
	BVN         string
	DateOfBirth time.Time
}

func (s *Service) SubmitBVN(ctx context.Context, req SubmitBVNRequest) (store.User, error) {
	if !bvnRe.MatchString(req.BVN) {
		return store.User{}, ErrInvalidBVN
	}
	if req.DateOfBirth.IsZero() || req.DateOfBirth.After(time.Now().AddDate(-13, 0, 0)) {
		return store.User{}, ErrInvalidDOB
	}

	if err := s.p.Verify(ctx, req.BVN, req.FullName, req.DateOfBirth); err != nil {
		s.log.Warn("kyc provider rejected", "err", err)
		return store.User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.BVN), bcrypt.DefaultCost)
	if err != nil {
		return store.User{}, fmt.Errorf("hash bvn: %w", err)
	}
	hashStr := string(hash)
	fullName := req.FullName

	return s.q.UpdateUserKYC(ctx, store.UpdateUserKYCParams{
		ID:          pgconv.UUIDFrom(req.UserID),
		FullName:    &fullName,
		BvnHash:     &hashStr,
		DateOfBirth: pgconv.DateFrom(req.DateOfBirth),
		KycTier:     1,
		KycStatus:   "verified",
	})
}

// ---- StubProvider ----

type StubProvider struct{}

func (StubProvider) Verify(_ context.Context, _, _ string, _ time.Time) error {
	// Format checks already happened in the service. The stub trusts.
	return nil
}

// ---- YouVerifyProvider ----

type YouVerifyProvider struct {
	client *YouVerifyClient
}

func NewYouVerifyProvider(c *YouVerifyClient) *YouVerifyProvider {
	return &YouVerifyProvider{client: c}
}

func (p *YouVerifyProvider) Verify(ctx context.Context, bvn, fullName string, dob time.Time) error {
	resp, err := p.client.LookupBVN(ctx, bvn)
	if err != nil {
		if errors.Is(err, ErrBVNNotFound) {
			return ErrBVNMismatch
		}
		return fmt.Errorf("%w: %v", ErrProviderFailed, err)
	}

	// Name match — concatenate everything YouVerify returns and check that
	// every token from the user-supplied name appears somewhere.
	provider := strings.ToLower(strings.Join([]string{
		resp.Data.FirstName, resp.Data.MiddleName, resp.Data.LastName,
	}, " "))
	for _, tok := range strings.Fields(strings.ToLower(fullName)) {
		if tok == "" {
			continue
		}
		if !strings.Contains(provider, tok) {
			return ErrBVNMismatch
		}
	}

	// DOB match — formats vary; tolerate "1990-01-15" and "15-01-1990".
	if !sameDate(dob, resp.Data.DateOfBirth) {
		return ErrBVNMismatch
	}
	return nil
}

func sameDate(want time.Time, got string) bool {
	for _, fmt := range []string{"2006-01-02", "02-01-2006", "2006/01/02"} {
		if parsed, err := time.Parse(fmt, got); err == nil {
			return parsed.Year() == want.Year() &&
				parsed.Month() == want.Month() &&
				parsed.Day() == want.Day()
		}
	}
	return false
}
