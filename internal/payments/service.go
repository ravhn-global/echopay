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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ravhn/echoapp-backend/internal/audit"
	"github.com/ravhn/echoapp-backend/internal/ledger"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/push"
	"github.com/ravhn/echoapp-backend/internal/risk"
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
	ErrNotSender           = errors.New("only the sender can undo")
	ErrUndoWindowClosed    = errors.New("undo window has expired")
	ErrUndoUnavailable     = errors.New("payment is not in an undoable state")
)

type Service struct {
	q       store.Querier
	tokens  *tokens.Service
	ledger  *ledger.Service
	trusted *risk.TrustedService
	ps      *paystack.Client
	push    *push.Service
	audit   *audit.Service
	log     *slog.Logger

	// In-process release timers, keyed by payment id. Survives single-
	// instance crashes via the reconciler safety net (ReleaseDueHolds).
	holdMu     sync.Mutex
	holdTimers map[uuid.UUID]*time.Timer
}

func NewService(
	q store.Querier,
	tokensSvc *tokens.Service,
	ledgerSvc *ledger.Service,
	trustedSvc *risk.TrustedService,
	ps *paystack.Client,
	pushSvc *push.Service,
	auditSvc *audit.Service,
	log *slog.Logger,
) *Service {
	return &Service{
		q: q, tokens: tokensSvc, ledger: ledgerSvc, trusted: trustedSvc,
		ps: ps, push: pushSvc, audit: auditSvc, log: log,
		holdTimers: make(map[uuid.UUID]*time.Timer),
	}
}

type CreateRequest struct {
	SenderUserID   uuid.UUID
	TokenCode      string
	MandateID      uuid.UUID
	IdempotencyKey string
	// UndoWindow > 0 puts the payment into a "held" state after the charge
	// succeeds: the receiver's transfer is deferred for that window and the
	// sender can call Undo within it to refund the charge before any money
	// leaves the float. v1.1 feature; clients send the user's preference
	// (default ₦500,000 threshold → 10s window, 0 below threshold).
	UndoWindow time.Duration
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
	if err := s.enforceLimits(ctx, sender, pgconv.UUIDTo(receiver.ID), tok.AmountKobo); err != nil {
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

	// Held path — charge fires immediately, transfer is deferred for the
	// undo window. The receiver gets nothing until release; if the sender
	// undoes inside the window, we refund the charge and the transfer
	// never runs. Only enabled when the client asks for it (the design's
	// >= ₦500k threshold rule lives client-side, not here).
	if req.UndoWindow > 0 {
		held, err := s.orchestrateHeld(ctx, payment, sender, receiver, mandate, &tok, req.UndoWindow)
		if err != nil {
			return nil, err
		}
		return &Result{Payment: held, Token: &tok}, nil
	}

	settled, err := s.orchestrate(ctx, payment, sender, receiver, mandate, &tok)
	if err != nil {
		return nil, err
	}
	return &Result{Payment: settled, Token: &tok}, nil
}

// orchestrateHeld runs the charge leg, parks the payment in 'held' state,
// schedules a goroutine to release it after the window, and returns. The
// scheduled goroutine runs the transfer+ledger+settle leg; until then,
// undo can intercept and refund the charge.
func (s *Service) orchestrateHeld(
	ctx context.Context,
	payment store.Payment,
	sender, receiver store.User,
	mandate store.Mandate,
	tok *store.Token,
	window time.Duration,
) (store.Payment, error) {
	if err := s.charge(ctx, &payment, sender, mandate); err != nil {
		return store.Payment{}, s.fail(ctx, payment, tok, fmt.Sprintf("charge: %v", err))
	}
	expiresAt := time.Now().Add(window)
	held, err := s.q.HoldPayment(ctx, store.HoldPaymentParams{
		ID:            payment.ID,
		HoldExpiresAt: pgconv.TimeFrom(expiresAt),
	})
	if err != nil {
		return store.Payment{}, fmt.Errorf("hold payment: %w", err)
	}

	s.scheduleHoldRelease(pgconv.UUIDTo(held.ID), window)
	return held, nil
}

// scheduleHoldRelease parks an in-process timer that fires the deferred
// transfer when the undo window elapses. The reconciler safety-net method
// ReleaseDueHolds picks up the slack if the process restarts.
func (s *Service) scheduleHoldRelease(id uuid.UUID, after time.Duration) {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	if existing, ok := s.holdTimers[id]; ok {
		existing.Stop()
	}
	s.holdTimers[id] = time.AfterFunc(after, func() {
		s.holdMu.Lock()
		delete(s.holdTimers, id)
		s.holdMu.Unlock()
		// Use a background ctx — the original request is long gone by now.
		s.releaseHold(context.Background(), id)
	})
}

// releaseHold flips status held → transferring, runs the transfer + ledger
// + settled completion. Conditional update on the held → transferring
// transition means undo can race here and only one wins.
func (s *Service) releaseHold(ctx context.Context, id uuid.UUID) {
	released, err := s.q.ReleaseHeldPayment(ctx, pgconv.UUIDFrom(id))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("release hold: update failed", "id", id, "err", err)
		}
		return // already refunded (lost the race) or row vanished
	}

	receiver, err := s.q.GetUserByID(ctx, released.ReceiverUserID)
	if err != nil {
		s.log.Error("release hold: load receiver", "id", id, "err", err)
		return
	}
	sender, err := s.q.GetUserByID(ctx, released.SenderUserID)
	if err != nil {
		s.log.Error("release hold: load sender", "id", id, "err", err)
		return
	}
	var tok *store.Token
	if released.TokenID.Valid {
		t, err := s.q.GetTokenByID(ctx, released.TokenID)
		if err == nil {
			tok = &t
		}
	}

	// Re-enter the standard tail: transfer, ledger, mark settled, push.
	if err := s.transfer(ctx, &released, receiver); err != nil {
		_ = s.fail(ctx, released, tok, fmt.Sprintf("transfer (post-hold): %v", err))
		return
	}
	chargeRef := ""
	if released.ChargeReference != nil {
		chargeRef = *released.ChargeReference
	}
	if err := s.ledger.RecordPaymentPair(
		ctx,
		pgconv.UUIDTo(released.ID),
		pgconv.UUIDTo(released.SenderUserID),
		pgconv.UUIDTo(released.ReceiverUserID),
		released.AmountKobo,
		chargeRef,
	); err != nil {
		_ = s.fail(ctx, released, tok, fmt.Sprintf("ledger (post-hold): %v", err))
		return
	}
	settled, err := s.q.MarkPaymentSettled(ctx, released.ID)
	if err != nil {
		s.log.Error("release hold: mark settled", "id", id, "err", err)
		return
	}
	if tok != nil {
		_, _ = s.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
			ID:               tok.ID,
			SettledPaymentID: settled.ID,
		})
	}

	// Same post-settle side effects as the synchronous path.
	senderName := "Someone"
	if sender.FullName != nil && *sender.FullName != "" {
		senderName = *sender.FullName
	} else if sender.Phone != "" {
		senderName = sender.Phone
	}
	s.push.NotifyPaymentReceived(ctx, settled, senderName)
}

// ReleaseDueHolds is the reconciler safety net — runs each tick and picks
// up any held payments whose in-process timer was lost across a restart.
// Idempotent: ReleaseHeldPayment is conditional on status=held, so an
// already-released row is a no-op.
func (s *Service) ReleaseDueHolds(ctx context.Context, batchSize int32) (int, error) {
	rows, err := s.q.ListExpiredHolds(ctx, store.ListExpiredHoldsParams{
		HoldExpiresAt: pgconv.TimeFrom(time.Now()),
		Limit:         batchSize,
	})
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		s.releaseHold(ctx, pgconv.UUIDTo(row.ID))
	}
	return len(rows), nil
}

// UndoRequest mirrors POST /v1/payments/:id/undo.
type UndoRequest struct {
	PaymentID uuid.UUID
	CallerID  uuid.UUID
}

// Undo refunds a held payment's charge before the window elapses. The DB
// transition status=held → status=refunded is conditional, so this races
// safely against the release timer and against itself.
func (s *Service) Undo(ctx context.Context, req UndoRequest) (store.Payment, error) {
	p, err := s.q.GetPaymentByID(ctx, pgconv.UUIDFrom(req.PaymentID))
	if err != nil {
		return store.Payment{}, fmt.Errorf("load payment: %w", err)
	}
	if pgconv.UUIDTo(p.SenderUserID) != req.CallerID {
		return store.Payment{}, ErrNotSender
	}
	if p.Status != "held" {
		return store.Payment{}, ErrUndoUnavailable
	}
	if !p.HoldExpiresAt.Valid || pgconv.TimeTo(p.HoldExpiresAt).Before(time.Now()) {
		return store.Payment{}, ErrUndoWindowClosed
	}
	if p.ChargeReference == nil || *p.ChargeReference == "" {
		return store.Payment{}, ErrUndoUnavailable
	}

	refund, err := s.ps.RefundCharge(ctx, paystack.RefundRequest{
		Transaction:  *p.ChargeReference,
		Amount:       p.AmountKobo,
		MerchantNote: "undo within window",
		CustomerNote: "EchoPay payment undone",
	})
	if err != nil {
		return store.Payment{}, fmt.Errorf("paystack refund: %w", err)
	}

	reason := "undone within window"
	updated, err := s.q.MarkPaymentRefunded(ctx, store.MarkPaymentRefundedParams{
		ID:                  p.ID,
		AutoRefundReference: &refund.Reference,
		FailureReason:       &reason,
	})
	if err != nil {
		// Race with the release timer — the timer won. The refund call
		// already went through Paystack, though; in practice this means
		// we've refunded an actual transfer that may still ship out.
		// Surface as an explicit error so the caller doesn't claim
		// success on a state that didn't settle.
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Payment{}, ErrUndoWindowClosed
		}
		return store.Payment{}, fmt.Errorf("mark refunded: %w", err)
	}

	// Cancel the parked timer if it hasn't fired yet — harmless if it has.
	s.holdMu.Lock()
	if t, ok := s.holdTimers[req.PaymentID]; ok {
		t.Stop()
		delete(s.holdTimers, req.PaymentID)
	}
	s.holdMu.Unlock()

	// Mark the associated token failed so the receiver UI doesn't keep
	// waiting on a payment that will never arrive.
	if updated.TokenID.Valid {
		_, _ = s.q.MarkTokenFailed(ctx, updated.TokenID)
	}

	s.audit.Record(ctx, audit.Event{
		UserID:    pgconv.UUIDTo(updated.SenderUserID),
		ActorID:   pgconv.UUIDTo(updated.SenderUserID),
		Action:    audit.ActionRefundIssued,
		TargetTyp: "payment",
		TargetID:  pgconv.UUIDTo(updated.ID).String(),
		Metadata: map[string]any{
			"reason":      "undo",
			"amount_kobo": updated.AmountKobo,
		},
	})
	return updated, nil
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
	if err := s.enforceLimits(ctx, refunder, pgconv.UUIDTo(originalSender.ID), req.AmountKobo); err != nil {
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

	// "Payment received" push to the receiver. Fire-and-forget — failure
	// here doesn't roll back the settled state.
	senderName := "Someone"
	if sender.FullName != nil && *sender.FullName != "" {
		senderName = *sender.FullName
	} else if sender.Phone != "" {
		senderName = sender.Phone
	}
	s.push.NotifyPaymentReceived(ctx, settled, senderName)

	// Audit on both sides — each user reads their own trail with a simple
	// WHERE user_id = ?.
	senderID := pgconv.UUIDTo(settled.SenderUserID)
	receiverID := pgconv.UUIDTo(settled.ReceiverUserID)
	pid := pgconv.UUIDTo(settled.ID).String()
	for _, side := range []struct {
		userID uuid.UUID
		role   string
	}{{senderID, "sender"}, {receiverID, "receiver"}} {
		counterparty := senderID
		if side.userID == senderID {
			counterparty = receiverID
		}
		s.audit.Record(ctx, audit.Event{
			UserID:    side.userID,
			ActorID:   senderID,
			Action:    audit.ActionPaymentSettled,
			TargetTyp: "payment",
			TargetID:  pid,
			Metadata: map[string]any{
				"amount_kobo":  settled.AmountKobo,
				"role":         side.role,
				"counterparty": counterparty.String(),
			},
		})
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

func (s *Service) enforceLimits(ctx context.Context, sender store.User, receiverID uuid.UUID, amount int64) error {
	effectiveTxCap := s.trusted.EffectiveCap(ctx, pgconv.UUIDTo(sender.ID), receiverID, sender.PerTxLimitKobo)
	if amount > effectiveTxCap {
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
	s.audit.Record(ctx, audit.Event{
		UserID:    pgconv.UUIDTo(p.SenderUserID),
		ActorID:   pgconv.UUIDTo(p.SenderUserID),
		Action:    audit.ActionPaymentFailed,
		TargetTyp: "payment",
		TargetID:  pgconv.UUIDTo(p.ID).String(),
		Metadata:  map[string]any{"amount_kobo": p.AmountKobo, "reason": reason},
	})
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
