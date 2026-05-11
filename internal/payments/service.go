// Package payments orchestrates the full settlement flow:
//
//	claim token -> verify limits -> create payment row (idempotent)
//	     -> Paystack charge_authorization (debit sender)
//	     -> Paystack transfer (credit receiver's NUBAN)
//	     -> ledger debit/credit pair
//	     -> mark payment settled + token settled
//
// Refunds reuse the same plumbing minus the token claim: the receiver of the
// original payment becomes the sender of a new payment back to the original
// sender, linked via refunds_payment_id.
//
// Failures at any step mark the payment 'failed' and (for token-backed flows)
// the token too. v1 is synchronous; later we move charge+transfer to a queue.
package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
	ErrNotReceiver         = errors.New("only the original receiver can refund")
	ErrRefundTooLarge      = errors.New("refund amount exceeds original payment")
	ErrOriginalNotSettled  = errors.New("can only refund settled payments")
	ErrAlreadyRefunded     = errors.New("payment already refunded")
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
	SenderUserID   uuid.UUID
	TokenCode      string
	MandateID      uuid.UUID
	IdempotencyKey string
}

type RefundRequest struct {
	OriginalPaymentID uuid.UUID
	RefunderUserID    uuid.UUID // must equal original receiver
	MandateID         uuid.UUID
	AmountKobo        int64 // may be < original.amount for partial refunds
	IdempotencyKey    string
}

type Result struct {
	Payment store.Payment
	Token   *store.Token // nil for refunds
}

// Create orchestrates a sender-initiated payment via an ultrasonic/QR token.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Result, error) {
	if existing, err := s.idempotentReturn(ctx, req.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	sender, err := s.q.GetUserByID(ctx, pgconv.UUIDFrom(req.SenderUserID))
	if err != nil {
		return nil, fmt.Errorf("load sender: %w", err)
	}
	mandate, err := s.validatedMandate(ctx, req.MandateID, req.SenderUserID)
	if err != nil {
		return nil, err
	}

	tok, err := s.tokens.Claim(ctx, req.TokenCode, req.SenderUserID)
	if err != nil {
		return nil, err
	}

	receiver, err := s.q.GetUserByID(ctx, tok.IssuedByUserID)
	if err != nil {
		return nil, fmt.Errorf("load receiver: %w", err)
	}
	if !hasBankDetails(receiver) {
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, ErrReceiverNoBank
	}
	if err := s.enforceLimits(ctx, sender, tok.AmountKobo); err != nil {
		_, _ = s.q.MarkTokenFailed(ctx, tok.ID)
		return nil, err
	}

	payment, err := s.q.CreatePayment(ctx, store.CreatePaymentParams{
		TokenID:          tok.ID,
		SenderUserID:     pgconv.UUIDFrom(req.SenderUserID),
		ReceiverUserID:   tok.IssuedByUserID,
		SenderMandateID:  pgconv.UUIDFrom(req.MandateID),
		AmountKobo:       tok.AmountKobo,
		IdempotencyKey:   req.IdempotencyKey,
		RefundsPaymentID: pgtype.UUID{},
	})
	if err != nil {
		return nil, fmt.Errorf("create payment: %w", err)
	}

	settled, err := s.orchestrate(ctx, payment, sender, receiver, mandate, &tok)
	if err != nil {
		return nil, err
	}
	return &Result{Payment: settled, Token: &tok}, nil
}

// Refund creates a new payment from the original receiver back to the original
// sender. The original sender must have a NUBAN configured to receive funds.
func (s *Service) Refund(ctx context.Context, req RefundRequest) (*Result, error) {
	if existing, err := s.idempotentReturn(ctx, req.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	original, err := s.q.GetPaymentByID(ctx, pgconv.UUIDFrom(req.OriginalPaymentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("original payment not found")
		}
		return nil, fmt.Errorf("load original: %w", err)
	}
	if pgconv.UUIDTo(original.ReceiverUserID) != req.RefunderUserID {
		return nil, ErrNotReceiver
	}
	if original.Status != "settled" {
		return nil, ErrOriginalNotSettled
	}
	if req.AmountKobo <= 0 || req.AmountKobo > original.AmountKobo {
		return nil, ErrRefundTooLarge
	}

	// Refunder (now sender) must have an active mandate.
	mandate, err := s.validatedMandate(ctx, req.MandateID, req.RefunderUserID)
	if err != nil {
		return nil, err
	}
	refunder, err := s.q.GetUserByID(ctx, pgconv.UUIDFrom(req.RefunderUserID))
	if err != nil {
		return nil, fmt.Errorf("load refunder: %w", err)
	}

	// Original sender (now receiver of refund) must have NUBAN configured.
	originalSender, err := s.q.GetUserByID(ctx, original.SenderUserID)
	if err != nil {
		return nil, fmt.Errorf("load original sender: %w", err)
	}
	if !hasBankDetails(originalSender) {
		return nil, ErrReceiverNoBank
	}
	if err := s.enforceLimits(ctx, refunder, req.AmountKobo); err != nil {
		return nil, err
	}

	payment, err := s.q.CreatePayment(ctx, store.CreatePaymentParams{
		TokenID:          pgtype.UUID{}, // refunds have no token
		SenderUserID:     pgconv.UUIDFrom(req.RefunderUserID),
		ReceiverUserID:   original.SenderUserID,
		SenderMandateID:  pgconv.UUIDFrom(req.MandateID),
		AmountKobo:       req.AmountKobo,
		IdempotencyKey:   req.IdempotencyKey,
		RefundsPaymentID: pgconv.UUIDFrom(req.OriginalPaymentID),
	})
	if err != nil {
		return nil, fmt.Errorf("create refund payment: %w", err)
	}

	settled, err := s.orchestrate(ctx, payment, refunder, originalSender, mandate, nil)
	if err != nil {
		return nil, err
	}
	return &Result{Payment: settled, Token: nil}, nil
}

// orchestrate runs the charge + transfer + ledger pipeline shared by Create
// and Refund. If tok is non-nil, its status is updated alongside the payment.
func (s *Service) orchestrate(
	ctx context.Context,
	payment store.Payment,
	sender, receiver store.User,
	mandate store.Mandate,
	tok *store.Token,
) (store.Payment, error) {
	if err := s.charge(ctx, &payment, sender, mandate); err != nil {
		return store.Payment{}, s.fail(ctx, payment, tok, fmt.Sprintf("charge: %v", err))
	}
	if err := s.transfer(ctx, &payment, receiver); err != nil {
		return store.Payment{}, s.fail(ctx, payment, tok, fmt.Sprintf("transfer: %v", err))
	}

	chargeRef := ""
	if payment.ChargeReference != nil {
		chargeRef = *payment.ChargeReference
	}
	if err := s.ledger.RecordPaymentPair(
		ctx,
		pgconv.UUIDTo(payment.ID),
		pgconv.UUIDTo(payment.SenderUserID),
		pgconv.UUIDTo(payment.ReceiverUserID),
		payment.AmountKobo,
		chargeRef,
	); err != nil {
		return store.Payment{}, s.fail(ctx, payment, tok, fmt.Sprintf("ledger: %v", err))
	}

	settled, err := s.q.MarkPaymentSettled(ctx, payment.ID)
	if err != nil {
		return store.Payment{}, fmt.Errorf("mark settled: %w", err)
	}
	if tok != nil {
		if _, err := s.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
			ID:               tok.ID,
			SettledPaymentID: settled.ID,
		}); err != nil {
			return store.Payment{}, fmt.Errorf("mark token settled: %w", err)
		}
	}
	return settled, nil
}

func (s *Service) idempotentReturn(ctx context.Context, key string) (*Result, error) {
	existing, err := s.q.GetPaymentByIdempotencyKey(ctx, key)
	if err == nil {
		return &Result{Payment: existing}, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return nil, fmt.Errorf("idempotency lookup: %w", err)
}

func (s *Service) validatedMandate(ctx context.Context, mandateID, userID uuid.UUID) (store.Mandate, error) {
	mandate, err := s.q.GetMandateByID(ctx, pgconv.UUIDFrom(mandateID))
	if err != nil {
		return store.Mandate{}, fmt.Errorf("load mandate: %w", err)
	}
	if pgconv.UUIDTo(mandate.UserID) != userID {
		return store.Mandate{}, ErrMandateNotOwned
	}
	if mandate.Status != "active" {
		return store.Mandate{}, ErrMandateNotActive
	}
	return mandate, nil
}

func (s *Service) enforceLimits(ctx context.Context, sender store.User, amount int64) error {
	if amount > sender.PerTxLimitKobo {
		return ErrTxLimitExceeded
	}
	sentLast24h, err := s.q.SumUserSentLast24h(ctx, sender.ID)
	if err != nil {
		return fmt.Errorf("sum sent: %w", err)
	}
	if sentLast24h+amount > sender.PerDayLimitKobo {
		return ErrDailyLimitExceeded
	}
	return nil
}

func (s *Service) charge(ctx context.Context, p *store.Payment, sender store.User, m store.Mandate) error {
	resp, err := s.ps.ChargeAuthorization(ctx, paystack.ChargeAuthorizationRequest{
		Email:             senderEmail(sender),
		Amount:            p.AmountKobo,
		AuthorizationCode: m.AuthorizationCode,
		Reference:         "CHG_" + pgconv.UUIDTo(p.ID).String(),
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
	resp, err := s.ps.InitiateTransfer(ctx, paystack.TransferRequest{
		Source:    "balance",
		Amount:    p.AmountKobo,
		Recipient: recipient.RecipientCode,
		Reason:    "Echo payment",
		Reference: "TXF_" + pgconv.UUIDTo(p.ID).String(),
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

func (s *Service) fail(ctx context.Context, p store.Payment, tok *store.Token, reason string) error {
	s.log.Warn("payment failed", "payment_id", pgconv.UUIDTo(p.ID), "reason", reason)
	if _, err := s.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
		ID:            p.ID,
		FailureReason: &reason,
	}); err != nil {
		s.log.Error("mark payment failed", "err", err)
	}
	if tok != nil {
		if _, err := s.q.MarkTokenFailed(ctx, tok.ID); err != nil {
			s.log.Error("mark token failed", "err", err)
		}
	}
	return errors.New(reason)
}

func hasBankDetails(u store.User) bool {
	return u.Nuban != nil && *u.Nuban != "" && u.BankCode != nil && *u.BankCode != ""
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

// ListUserActivity returns paginated payment history for a user.
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
