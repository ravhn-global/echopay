// Package kyc handles identity verification.
//
// v1: stub validation only (BVN format check, DOB present). Production would
// call YouVerify or similar to confirm the BVN actually belongs to the user.
package kyc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

var (
	ErrInvalidBVN = errors.New("BVN must be 11 digits")
	ErrInvalidDOB = errors.New("invalid date of birth")
)

var bvnRe = regexp.MustCompile(`^\d{11}$`)

type Service struct {
	q store.Querier
}

func NewService(q store.Querier) *Service {
	return &Service{q: q}
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
