// Package mandates manages Paystack authorization codes that let us debit
// a user's bank account or card without their per-transaction approval.
//
// Flow:
//
//  1. Client calls InitMandate -> we open a tiny ₦50 Paystack hosted page
//     transaction. Returns authorization_url.
//  2. User completes the Paystack flow in a webview / external browser.
//  3. Client calls VerifyMandate(reference) -> we verify the transaction
//     server-side and save the returned authorization_code as a mandate.
//
// Subsequent payments hit Paystack /transaction/charge_authorization
// using the saved authorization_code.
package mandates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

const setupAmountKobo int64 = 5000 // ₦50.00 used to capture the authorization

var (
	ErrAuthorizationNotReusable = errors.New("paystack returned a non-reusable authorization")
	ErrChargeNotSuccessful      = errors.New("paystack charge was not successful")
)

type Service struct {
	q  store.Querier
	ps *paystack.Client
}

func NewService(q store.Querier, ps *paystack.Client) *Service {
	return &Service{q: q, ps: ps}
}

type InitResult struct {
	AuthorizationURL string
	Reference        string
}

func (s *Service) InitMandate(ctx context.Context, userID uuid.UUID, email string) (*InitResult, error) {
	if email == "" {
		// Paystack requires an email; if user only has a phone we fabricate.
		email = userID.String() + "@echo.users"
	}
	ref := "MAN_" + randomToken(12)
	resp, err := s.ps.InitializeTransaction(ctx, paystack.InitializeRequest{
		Email:     email,
		Amount:    setupAmountKobo,
		Reference: ref,
		Channels:  []string{"bank", "card"},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize paystack tx: %w", err)
	}
	return &InitResult{AuthorizationURL: resp.AuthorizationURL, Reference: resp.Reference}, nil
}

func (s *Service) VerifyMandate(ctx context.Context, userID uuid.UUID, reference string) (store.Mandate, error) {
	tx, err := s.ps.VerifyTransaction(ctx, reference)
	if err != nil {
		return store.Mandate{}, fmt.Errorf("verify paystack tx: %w", err)
	}
	if !strings.EqualFold(tx.Status, "success") {
		return store.Mandate{}, ErrChargeNotSuccessful
	}
	if !tx.Authorization.Reusable {
		return store.Mandate{}, ErrAuthorizationNotReusable
	}

	// First mandate becomes the default.
	existing, _ := s.q.ListUserMandates(ctx, pgconv.UUIDFrom(userID))
	isDefault := len(existing) == 0
	if isDefault {
		_ = s.q.UnsetDefaultMandate(ctx, pgconv.UUIDFrom(userID))
	}

	bankCode := tx.Authorization.Bin // Paystack doesn't return bank code directly here; bin is a fallback
	mandate, err := s.q.CreateMandate(ctx, store.CreateMandateParams{
		UserID:            pgconv.UUIDFrom(userID),
		AuthorizationCode: tx.Authorization.AuthorizationCode,
		BankName:          tx.Authorization.Bank,
		BankCode:          &bankCode,
		Last4:             tx.Authorization.Last4,
		Channel:           tx.Authorization.Channel,
		Reusable:          tx.Authorization.Reusable,
		IsDefault:         isDefault,
	})
	if err != nil {
		return store.Mandate{}, fmt.Errorf("save mandate: %w", err)
	}
	return mandate, nil
}

func (s *Service) ListMandates(ctx context.Context, userID uuid.UUID) ([]store.Mandate, error) {
	return s.q.ListUserMandates(ctx, pgconv.UUIDFrom(userID))
}

func (s *Service) SetDefault(ctx context.Context, userID, mandateID uuid.UUID) (store.Mandate, error) {
	if err := s.q.UnsetDefaultMandate(ctx, pgconv.UUIDFrom(userID)); err != nil {
		return store.Mandate{}, fmt.Errorf("unset default: %w", err)
	}
	return s.q.SetDefaultMandate(ctx, store.SetDefaultMandateParams{
		ID:     pgconv.UUIDFrom(mandateID),
		UserID: pgconv.UUIDFrom(userID),
	})
}

func (s *Service) Revoke(ctx context.Context, userID, mandateID uuid.UUID) error {
	return s.q.RevokeMandate(ctx, store.RevokeMandateParams{
		ID:     pgconv.UUIDFrom(mandateID),
		UserID: pgconv.UUIDFrom(userID),
	})
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
