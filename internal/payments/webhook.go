package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

// HandleWebhookEvent applies a Paystack webhook to our state idempotently.
// Webhooks may arrive multiple times, out of order, or after our synchronous
// flow has already reached a terminal state — every branch must tolerate that.
func (s *Service) HandleWebhookEvent(ctx context.Context, ev *paystack.WebhookEvent) error {
	switch ev.Event {
	case "charge.success", "charge.failed":
		d, err := ev.AsCharge()
		if err != nil {
			return fmt.Errorf("decode charge event: %w", err)
		}
		return s.applyChargeEvent(ctx, d)

	case "transfer.success", "transfer.failed", "transfer.reversed":
		d, err := ev.AsTransfer()
		if err != nil {
			return fmt.Errorf("decode transfer event: %w", err)
		}
		return s.applyTransferEvent(ctx, d)

	default:
		s.log.Debug("paystack webhook ignored", "event", ev.Event)
		return nil
	}
}

func (s *Service) applyChargeEvent(ctx context.Context, d *paystack.ChargeEventData) error {
	if d.Reference == "" {
		return nil
	}
	p, err := s.q.GetPaymentByChargeReference(ctx, &d.Reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("charge webhook: no matching payment", "reference", d.Reference)
			return nil
		}
		return fmt.Errorf("lookup payment by charge ref: %w", err)
	}
	// Already settled or failed — nothing to do.
	if p.Status == "settled" || p.Status == "failed" {
		return nil
	}
	// We already record the charge synchronously; the webhook is mostly
	// confirmation. If the webhook says failed but we marked it transferring,
	// flip to failed.
	if d.Status == "failed" && p.Status != "settled" {
		reason := "charge.failed via webhook: " + d.GatewayResp
		_, err := s.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
			ID:            p.ID,
			FailureReason: &reason,
		})
		return err
	}
	return nil
}

func (s *Service) applyTransferEvent(ctx context.Context, d *paystack.TransferEventData) error {
	if d.Reference == "" {
		return nil
	}
	p, err := s.q.GetPaymentByTransferReference(ctx, &d.Reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("transfer webhook: no matching payment", "reference", d.Reference)
			return nil
		}
		return fmt.Errorf("lookup payment by transfer ref: %w", err)
	}
	if p.Status == "settled" || p.Status == "failed" {
		return nil
	}

	transferStatus := d.Status
	updated, err := s.q.UpdatePaymentTransfer(ctx, store.UpdatePaymentTransferParams{
		ID:                p.ID,
		TransferReference: &d.Reference,
		TransferStatus:    &transferStatus,
		Status:            p.Status, // keep current orchestration phase
	})
	if err != nil {
		return fmt.Errorf("update transfer state: %w", err)
	}

	switch d.Status {
	case "success":
		// If we hadn't already written ledger entries in the sync flow, do it
		// now. RecordPaymentPair isn't idempotent on its own; we guard by
		// only writing if no ledger rows exist for this payment yet.
		entries, _ := s.q.ListPaymentLedger(ctx, updated.ID)
		if len(entries) == 0 {
			chargeRef := ""
			if updated.ChargeReference != nil {
				chargeRef = *updated.ChargeReference
			}
			if err := s.ledger.RecordPaymentPair(
				ctx,
				pgconv.UUIDTo(updated.ID),
				pgconv.UUIDTo(updated.SenderUserID),
				pgconv.UUIDTo(updated.ReceiverUserID),
				updated.AmountKobo,
				chargeRef,
			); err != nil {
				return fmt.Errorf("ledger via webhook: %w", err)
			}
		}
		if _, err := s.q.MarkPaymentSettled(ctx, updated.ID); err != nil {
			return fmt.Errorf("mark settled via webhook: %w", err)
		}
		if updated.TokenID.Valid {
			_, _ = s.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
				ID:               updated.TokenID,
				SettledPaymentID: updated.ID,
			})
		}
	case "failed", "reversed":
		reason := "transfer." + d.Status + " via webhook"
		if _, err := s.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
			ID:            updated.ID,
			FailureReason: &reason,
		}); err != nil {
			return fmt.Errorf("mark failed via webhook: %w", err)
		}
		if updated.TokenID.Valid {
			_, _ = s.q.MarkTokenFailed(ctx, updated.TokenID)
		}
	}
	return nil
}
