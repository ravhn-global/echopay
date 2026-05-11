// Package payments orchestrates the full settlement flow:
//
//	claim token -> verify limits -> create payment row (idempotent)
//	     -> Paystack charge_authorization (debit sender)
//	     -> Paystack transfer (credit receiver's NUBAN)
//	     -> ledger debit/credit pair
//	     -> mark payment settled + token settled
//
// Failures at any step mark the payment 'failed' with a reason and leave the
// token in 'failed' so the receiver knows. v1 is synchronous; later we move
// the charge+transfer to an asynq queue with retries.
package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ravhn/echoapp-backend/internal/ledger"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
	"github.com/ravhn/echoapp-backend/internal/tokens"
)

var (
	ErrTxLimitExceeded     = errors.New("amount exceeds per-transaction limit")
	ErrDailyLimitExceeded  = errors.New("amount exceeds 24h limit")
	ErrMandateNotActive    = errors.New("sender mandate not active")
	ErrMandateNotOwned     = errors.New("mandate not owned by sender")
	ErrReceiverNoBank      = errors.New("receiver has no NUBAN configured")
	ErrIdempotencyMismatch = errors.New("idempotency_key already used with different parameters")
)

type Service struct {
	q      store.Querier
	tokens *tokens.Service
	ledger *ledger.Service
	ps     *paystack.Client
	log    *slog.Logger
}

func NewService(
	q store.Querier,
	tokensSvc *tokens.Service,
	ledgerSvc *ledger.Service,
	ps *paystack.Client,
	log *slog.Logger,
) *Service {
	return &Service{q: q, tokens: tokensSvc, ledger: ledgerSvc, ps: ps, log: log}
}

type CreateRequest struct {
	SenderUserID    uuid.UUID
	TokenCode       string
	MandateID       uuid.UUID
	IdempotencyKey  string
}

type Result struct {
	Payment store.Payment
	Token   store.Token
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (*Result, error) {
	// Idempotency short-circuit.
	if existing, err := s.q.GetPaymentByIdempotencyKey(ctx, req.IdempotencyKey); err == nil {
		tok, err := s.q.GetTokenByID(ctx, existing.TokenID)
		if err != nil {
			return nil, fmt.Errorf("re-fetch token for idempotent payment: %w", err)
		}
		return &Result{Payment: existing, Token: tok}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}

	// Load sender.
	sender, err := s.q.GetUserByID(ctx, pgconv.UUIDFrom(req.SenderUserID))
	if err != nil {
		return nil, fmt.Errorf("load sender: %w", err)
	}

	// Validate mandate ownership and status.
	mandate, err := s.q.GetMandateByID(ctx, pgconv.UUIDFrom(req.MandateID))
	if err != nil {
		return nil, fmt.Errorf("load mandate: %w", err)
	}
	if pgconv.UUIDTo(mandate.UserID) != req.SenderUserID {
		return nil, ErrMandateNotOwned
	}
	if mandate.Status != "active" {
		return nil, ErrMandateNotActive
	}

	// Claim the token atomically (fails if already taken or expired).
	tok, err := s.tokens.Claim(ctx, req.TokenCode, req.SenderUserID)
	if err != nil {
		return nil, err
	}

	// Load receiver (must have NUBAN configured to receive a transfer).
	receiver, err := s.q.GetUserByID(ctx, tok.IssuedByUserID)
	if err != nil {
		return nil, fmt.Errorf("load receiver: %w", err)
	}
	if receiver.Nuban == nil || *receiver.Nuban == "" || receiver.BankCode == nil || *receiver.BankCode == "" {
		// Token was claimed; mark failed to release the receiver's UI.
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, ErrReceiverNoBank
	}

	// Limit checks.
	if tok.AmountKobo > sender.PerTxLimitKobo {
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, ErrTxLimitExceeded
	}
	sentLast24h, err := s.q.SumUserSentLast24h(ctx, pgconv.UUIDFrom(req.SenderUserID))
	if err != nil {
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, fmt.Errorf("sum sent: %w", err)
	}
	if sentLast24h+tok.AmountKobo > sender.PerDayLimitKobo {
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, ErrDailyLimitExceeded
	}

	// Create payment row.
	payment, err := s.q.CreatePayment(ctx, store.CreatePaymentParams{
		TokenID:         tok.ID,
		SenderUserID:    pgconv.UUIDFrom(req.SenderUserID),
		ReceiverUserID:  tok.IssuedByUserID,
		SenderMandateID: pgconv.UUIDFrom(req.MandateID),
		AmountKobo:      tok.AmountKobo,
		IdempotencyKey:  req.IdempotencyKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create payment: %w", err)
	}

	// Step 1: charge sender via authorization code.
	if err := s.charge(ctx, &payment, sender, mandate); err != nil {
		return nil, s.fail(ctx, payment, tok, fmt.Sprintf("charge: %v", err))
	}

	// Step 2: transfer to receiver.
	if err := s.transfer(ctx, &payment, receiver); err != nil {
		return nil, s.fail(ctx, payment, tok, fmt.Sprintf("transfer: %v", err))
	}

	// Step 3: ledger + mark settled.
	if err := s.ledger.RecordPaymentPair(ctx, pgconv.UUIDTo(payment.ID), req.SenderUserID, pgconv.UUIDTo(tok.IssuedByUserID), tok.AmountKobo, *payment.ChargeReference); err != nil {
		return nil, s.fail(ctx, payment, tok, fmt.Sprintf("ledger: %v", err))
	}
	settled, err := s.q.MarkPaymentSettled(ctx, payment.ID)
	if err != nil {
		return nil, fmt.Errorf("mark settled: %w", err)
	}
	settledTok, err := s.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
		ID:               tok.ID,
		SettledPaymentID: settled.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("mark token settled: %w", err)
	}

	return &Result{Payment: settled, Token: settledTok}, nil
}

func (s *Service) charge(ctx context.Context, p *store.Payment, sender store.User, m store.Mandate) error {
	email := senderEmail(sender)
	reference := "CHG_" + pgconv.UUIDTo(p.ID).String()
	resp, err := s.ps.ChargeAuthorization(ctx, paystack.ChargeAuthorizationRequest{
		Email:             email,
		Amount:            p.AmountKobo,
		AuthorizationCode: m.AuthorizationCode,
		Reference:         reference,
	})
	if err != nil {
		return err
	}
	if resp.Status != "success" {
		return fmt.Errorf("paystack charge status=%s: %s", resp.Status, resp.GatewayResp)
	}
	updated, err := s.q.UpdatePaymentCharge(ctx, store.UpdatePaymentChargeParams{
		ID:              p.ID,
		ChargeReference: &resp.Reference,
		ChargeStatus:    &resp.Status,
		Status:          "transferring",
	})
	if err != nil {
		return fmt.Errorf("update charge state: %w", err)
	}
	*p = updated
	return nil
}

func (s *Service) transfer(ctx context.Context, p *store.Payment, receiver store.User) error {
	recipient, err := s.ps.CreateRecipient(ctx, paystack.CreateRecipientRequest{
		Type:          "nuban",
		Name:          deref(receiver.AccountName, receiver.Phone),
		AccountNumber: deref(receiver.Nuban, ""),
		BankCode:      deref(receiver.BankCode, ""),
	})
	if err != nil {
		return fmt.Errorf("create recipient: %w", err)
	}
	reference := "TXF_" + pgconv.UUIDTo(p.ID).String()
	resp, err := s.ps.InitiateTransfer(ctx, paystack.TransferRequest{
		Source:    "balance",
		Amount:    p.AmountKobo,
		Recipient: recipient.RecipientCode,
		Reason:    "Echo payment",
		Reference: reference,
	})
	if err != nil {
		return fmt.Errorf("initiate transfer: %w", err)
	}
	updated, err := s.q.UpdatePaymentTransfer(ctx, store.UpdatePaymentTransferParams{
		ID:                p.ID,
		TransferReference: &resp.Reference,
		TransferStatus:    &resp.Status,
		Status:            "settling",
	})
	if err != nil {
		return fmt.Errorf("update transfer state: %w", err)
	}
	*p = updated
	return nil
}

func (s *Service) fail(ctx context.Context, p store.Payment, tok store.Token, reason string) error {
	s.log.Warn("payment failed", "payment_id", pgconv.UUIDTo(p.ID), "reason", reason)
	if _, err := s.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
		ID:            p.ID,
		FailureReason: &reason,
	}); err != nil {
		s.log.Error("mark payment failed", "err", err)
	}
	if _, err := s.q.MarkTokenFailed(ctx, tok.ID); err != nil {
		s.log.Error("mark token failed", "err", err)
	}
	return errors.New(reason)
}

func senderEmail(u store.User) string {
	return pgconv.UUIDTo(u.ID).String() + "@echo.users"
}

func deref(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// ListUserActivity returns the paginated payment history for a user
// (acting as either sender or receiver).
func (s *Service) ListUserActivity(ctx context.Context, userID uuid.UUID, limit, offset int32) ([]store.Payment, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.q.ListUserPayments(ctx, store.ListUserPaymentsParams{
		SenderUserID: pgconv.UUIDFrom(userID),
		Limit:        limit,
		Offset:       offset,
	})
}
