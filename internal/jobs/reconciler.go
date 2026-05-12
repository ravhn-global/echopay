// Package jobs runs periodic background work: reconciling stuck payments
// against Paystack's actual transaction state, and (eventually) triggering
// auto-refunds for payments that never finalize.
//
// v1 is a simple in-process time.Ticker. When we scale beyond one server we
// move to asynq (or similar) with leader election. The current design
// tolerates multiple instances running side-by-side because every operation
// is idempotent (status checks short-circuit, mark calls are write-then-skip).
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ravhn/echoapp-backend/internal/payments"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/risk"
	"github.com/ravhn/echoapp-backend/internal/store"
)

const (
	tickInterval         = 5 * time.Minute
	stuckAfter           = 10 * time.Minute  // poll Paystack for anything stuck >10min
	autoRefundAfter      = 24 * time.Hour    // mark for auto-refund after 24h stuck
	reconcileBatchSize   = 50
)

type Reconciler struct {
	q        store.Querier
	ps       *paystack.Client
	limits   *risk.LimitsService
	payments *payments.Service // for the held-payment safety net
	log      *slog.Logger
}

func NewReconciler(
	q store.Querier,
	ps *paystack.Client,
	limits *risk.LimitsService,
	paymentsSvc *payments.Service,
	log *slog.Logger,
) *Reconciler {
	return &Reconciler{q: q, ps: ps, limits: limits, payments: paymentsSvc, log: log}
}

// Run blocks until ctx is cancelled, sweeping stuck payments every tick.
// Two cadences: the slow 5-minute sweep for stuck Paystack flows, and a
// faster 2-second tick that releases held payments whose in-process
// timer was lost across a restart.
func (r *Reconciler) Run(ctx context.Context) {
	r.log.Info("reconciler started", "interval", tickInterval.String())
	slow := time.NewTicker(tickInterval)
	defer slow.Stop()
	fast := time.NewTicker(2 * time.Second)
	defer fast.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopped")
			return
		case <-slow.C:
			r.sweep(ctx)
		case <-fast.C:
			r.releaseDueHolds(ctx)
		}
	}
}

func (r *Reconciler) releaseDueHolds(ctx context.Context) {
	if _, err := r.payments.ReleaseDueHolds(ctx, reconcileBatchSize); err != nil {
		r.log.Error("release due holds", "err", err)
	}
}

func (r *Reconciler) sweep(ctx context.Context) {
	// Apply any limit-raise cooldowns that have elapsed since the last tick.
	if applied, err := r.limits.ApplyDue(ctx, reconcileBatchSize); err != nil {
		r.log.Error("apply due limits failed", "err", err)
	} else if applied > 0 {
		r.log.Info("limit changes applied", "count", applied)
	}

	cutoff := time.Now().Add(-stuckAfter)
	payments, err := r.q.ListStuckPayments(ctx, store.ListStuckPaymentsParams{
		UpdatedAt: pgconv.TimeFrom(cutoff),
		Limit:     reconcileBatchSize,
	})
	if err != nil {
		r.log.Error("reconciler list failed", "err", err)
		return
	}
	if len(payments) > 0 {
		r.log.Info("reconciler sweep", "count", len(payments))
	}
	for _, p := range payments {
		if err := r.reconcileOne(ctx, p); err != nil {
			r.log.Error("reconcile payment", "id", pgconv.UUIDTo(p.ID), "err", err)
		}
	}

	r.attemptAutoRefunds(ctx)
	r.expireStaleTokens(ctx)
}

// attemptAutoRefunds finds payments that have been stuck post-charge for
// >autoRefundAfter (default 24h) and reverses the sender's charge via
// Paystack. Idempotent — payments with auto_refund_reference set are
// excluded from the query, and we double-check Paystack's transfer state
// before refunding in case we just missed the success webhook.
func (r *Reconciler) attemptAutoRefunds(ctx context.Context) {
	cutoff := time.Now().Add(-autoRefundAfter)
	stuck, err := r.q.ListPaymentsForAutoRefund(ctx, store.ListPaymentsForAutoRefundParams{
		CreatedAt: pgconv.TimeFrom(cutoff),
		Limit:     reconcileBatchSize,
	})
	if err != nil {
		r.log.Error("list payments for auto-refund failed", "err", err)
		return
	}
	if len(stuck) == 0 {
		return
	}
	r.log.Info("auto-refund candidates", "count", len(stuck))

	for _, p := range stuck {
		if err := r.autoRefundOne(ctx, p); err != nil {
			r.log.Error("auto-refund payment", "id", pgconv.UUIDTo(p.ID), "err", err)
		}
	}
}

func (r *Reconciler) autoRefundOne(ctx context.Context, p store.Payment) error {
	// Double-check the transfer leg hasn't actually succeeded — we may just
	// have missed a webhook. If Paystack says it landed, reconcile to
	// settled rather than refunding.
	if p.TransferReference != nil && *p.TransferReference != "" {
		verified, err := r.ps.VerifyTransfer(ctx, *p.TransferReference)
		if err == nil && verified.Status == "success" {
			if _, mErr := r.q.MarkPaymentSettled(ctx, p.ID); mErr != nil {
				return mErr
			}
			if p.TokenID.Valid {
				_, _ = r.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
					ID:               p.TokenID,
					SettledPaymentID: p.ID,
				})
			}
			r.log.Info("auto-refund cancelled: transfer succeeded after all", "id", pgconv.UUIDTo(p.ID))
			return nil
		}
	}

	if p.ChargeReference == nil || *p.ChargeReference == "" {
		return errors.New("no charge reference to refund")
	}

	refund, err := r.ps.RefundCharge(ctx, paystack.RefundRequest{
		Transaction:  *p.ChargeReference,
		Amount:       p.AmountKobo,
		MerchantNote: "auto-refund: 24h stuck",
		CustomerNote: "Echo refund: payment couldn't be delivered",
	})
	if err != nil {
		return err
	}

	reason := "auto-refunded after 24h stuck"
	_, err = r.q.MarkPaymentAutoRefunded(ctx, store.MarkPaymentAutoRefundedParams{
		ID:                  p.ID,
		AutoRefundReference: &refund.Reference,
		FailureReason:       &reason,
	})
	if err != nil {
		return err
	}
	if p.TokenID.Valid {
		_, _ = r.q.MarkTokenFailed(ctx, p.TokenID)
	}
	r.log.Info("auto-refunded", "id", pgconv.UUIDTo(p.ID), "refund_ref", refund.Reference)
	return nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, p store.Payment) error {
	// If we have a transfer reference, that's the most recent step — check it.
	if p.TransferReference != nil && *p.TransferReference != "" {
		return r.reconcileTransfer(ctx, p)
	}
	// Otherwise check the charge.
	if p.ChargeReference != nil && *p.ChargeReference != "" {
		return r.reconcileCharge(ctx, p)
	}
	// No external reference — payment never made it out the door. Age it out.
	if pgconv.TimeTo(p.UpdatedAt).Before(time.Now().Add(-autoRefundAfter)) {
		reason := "never executed beyond initiated state"
		_, err := r.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
			ID:            p.ID,
			FailureReason: &reason,
		})
		return err
	}
	return nil
}

func (r *Reconciler) reconcileTransfer(ctx context.Context, p store.Payment) error {
	verified, err := r.ps.VerifyTransfer(ctx, *p.TransferReference)
	if err != nil {
		return err
	}
	switch verified.Status {
	case "success":
		if _, err := r.q.MarkPaymentSettled(ctx, p.ID); err != nil {
			return err
		}
		if p.TokenID.Valid {
			_, _ = r.q.MarkTokenSettled(ctx, store.MarkTokenSettledParams{
				ID:               p.TokenID,
				SettledPaymentID: p.ID,
			})
		}
		r.log.Info("reconciler settled payment", "id", pgconv.UUIDTo(p.ID))
	case "failed", "reversed":
		reason := "transfer." + verified.Status + " via reconciler"
		if _, err := r.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
			ID:            p.ID,
			FailureReason: &reason,
		}); err != nil {
			return err
		}
		if p.TokenID.Valid {
			_, _ = r.q.MarkTokenFailed(ctx, p.TokenID)
		}
		r.log.Info("reconciler marked failed", "id", pgconv.UUIDTo(p.ID), "status", verified.Status)
	default:
		// still pending — leave it for the next sweep
	}
	return nil
}

func (r *Reconciler) reconcileCharge(ctx context.Context, p store.Payment) error {
	verified, err := r.ps.VerifyTransaction(ctx, *p.ChargeReference)
	if err != nil {
		return err
	}
	if verified.Status == "failed" {
		reason := "charge failed via reconciler: " + verified.GatewayResp
		_, err := r.q.MarkPaymentFailed(ctx, store.MarkPaymentFailedParams{
			ID:            p.ID,
			FailureReason: &reason,
		})
		if p.TokenID.Valid {
			_, _ = r.q.MarkTokenFailed(ctx, p.TokenID)
		}
		return err
	}
	// success or pending — nothing further; transfer leg will be reconciled
	// on a subsequent sweep once the transfer reference is recorded.
	return nil
}

func (r *Reconciler) expireStaleTokens(ctx context.Context) {
	if err := r.q.ExpireStaleTokens(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		r.log.Error("expire stale tokens", "err", err)
	}
}

