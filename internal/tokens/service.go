// Package tokens issues short-lived session tokens that travel over the
// ultrasonic/QR channel between two phones.
//
// Lifecycle:
//
//	created  -> claimed  -> settled
//	created  -> claimed  -> failed
//	created  -> expired           (60s TTL hit without anyone claiming)
//
// Redis holds a hot copy keyed by code for 60s (cheap lookups + atomic claim
// via SETNX). Postgres holds the durable record (for activity history and
// reconciliation).
package tokens

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

const (
	TokenTTL = 60 * time.Second
	codeLen  = 8 // bytes -> 16 hex chars on the wire
)

var (
	ErrTokenNotFound = errors.New("token not found or expired")
	ErrTokenClaimed  = errors.New("token already claimed")
	ErrSelfClaim     = errors.New("cannot claim your own token")
)

type Service struct {
	q   store.Querier
	rdb *redis.Client
}

func NewService(q store.Querier, rdb *redis.Client) *Service {
	return &Service{q: q, rdb: rdb}
}

type IssueRequest struct {
	IssuedByUserID uuid.UUID
	AmountKobo     int64
	Note           string
}

type IssuedToken struct {
	ID         uuid.UUID
	Code       string
	AmountKobo int64
	ExpiresAt  time.Time
}

func (s *Service) Issue(ctx context.Context, req IssueRequest) (*IssuedToken, error) {
	code, err := generateCode()
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}
	expires := time.Now().Add(TokenTTL)

	var notePtr *string
	if req.Note != "" {
		notePtr = &req.Note
	}

	row, err := s.q.CreateToken(ctx, store.CreateTokenParams{
		Code:           code,
		IssuedByUserID: pgconv.UUIDFrom(req.IssuedByUserID),
		AmountKobo:     req.AmountKobo,
		Note:           notePtr,
		ExpiresAt:      pgconv.TimeFrom(expires),
	})
	if err != nil {
		return nil, fmt.Errorf("persist token: %w", err)
	}

	// Mirror in Redis for fast lookup. Key not strictly needed for correctness
	// (Postgres is source of truth via ClaimToken's atomic UPDATE), but it
	// dramatically reduces load when many senders are scanning.
	_ = s.rdb.Set(ctx, redisKey(code), pgconv.UUIDTo(row.ID).String(), TokenTTL).Err()

	return &IssuedToken{
		ID:         pgconv.UUIDTo(row.ID),
		Code:       code,
		AmountKobo: row.AmountKobo,
		ExpiresAt:  pgconv.TimeTo(row.ExpiresAt),
	}, nil
}

type Resolved struct {
	Token            store.Token
	Issuer           store.User
}

// Resolve looks up a token for the confirmation screen WITHOUT claiming it.
// Idempotent — safe to call multiple times.
func (s *Service) Resolve(ctx context.Context, code string) (*Resolved, error) {
	tok, err := s.q.GetTokenByCode(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("get token: %w", err)
	}
	if pgconv.TimeTo(tok.ExpiresAt).Before(time.Now()) {
		return nil, ErrTokenNotFound
	}
	issuer, err := s.q.GetUserByID(ctx, tok.IssuedByUserID)
	if err != nil {
		return nil, fmt.Errorf("get issuer: %w", err)
	}
	return &Resolved{Token: tok, Issuer: issuer}, nil
}

// Claim atomically transitions a token to 'claimed' if it's currently
// 'created' and not expired. Returns ErrTokenClaimed if someone else got there
// first.
func (s *Service) Claim(ctx context.Context, code string, claimerID uuid.UUID) (store.Token, error) {
	tok, err := s.q.GetTokenByCode(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Token{}, ErrTokenNotFound
		}
		return store.Token{}, fmt.Errorf("get token: %w", err)
	}
	if pgconv.UUIDTo(tok.IssuedByUserID) == claimerID {
		return store.Token{}, ErrSelfClaim
	}
	claimed, err := s.q.ClaimToken(ctx, store.ClaimTokenParams{
		ID:              tok.ID,
		ClaimedByUserID: pgconv.UUIDFrom(claimerID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// row exists but predicate failed: it's already claimed or expired
			return store.Token{}, ErrTokenClaimed
		}
		return store.Token{}, fmt.Errorf("claim token: %w", err)
	}
	_ = s.rdb.Del(ctx, redisKey(code)).Err()
	return claimed, nil
}

func generateCode() (string, error) {
	b := make([]byte, codeLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func redisKey(code string) string {
	return "token:" + code
}
