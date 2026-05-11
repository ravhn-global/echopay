// Package risk encapsulates the asymmetric-friction rules from the design
// system: lowering sending limits is instant; raising them schedules a
// pending change that lands after a cooldown (default 24h). Trusted merchant
// caps live here too — see trusted.go.
package risk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

var ErrLimitsInvalid = errors.New("per-tx limit cannot exceed daily limit")

type LimitsService struct {
	q        store.Querier
	cooldown time.Duration
	log      *slog.Logger
}

func NewLimitsService(q store.Querier, cooldown time.Duration, log *slog.Logger) *LimitsService {
	return &LimitsService{q: q, cooldown: cooldown, log: log}
}

// Outcome reports what happened to the request. If Applied is true the user's
// limits changed immediately; if false, Pending holds the scheduled change.
type Outcome struct {
	Applied bool
	User    *store.User
	Pending *store.PendingLimitChange
}

// SetLimits enforces the asymmetric rule:
//   - both new values ≤ current → apply now (cancel any pending raise)
//   - otherwise → schedule for cooldown later (replacing any prior pending)
func (s *LimitsService) SetLimits(ctx context.Context, userID uuid.UUID, perTxKobo, perDayKobo int64) (*Outcome, error) {
	if perTxKobo <= 0 || perDayKobo <= 0 {
		return nil, ErrLimitsInvalid
	}
	if perTxKobo > perDayKobo {
		return nil, ErrLimitsInvalid
	}

	user, err := s.q.GetUserByID(ctx, pgconv.UUIDFrom(userID))
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}

	isLoweringOrEqual := perTxKobo <= user.PerTxLimitKobo && perDayKobo <= user.PerDayLimitKobo

	// Always cancel any prior pending change — scheduling a new one
	// supersedes it, and an immediate-apply must also clear it.
	if err := s.q.CancelPendingLimitChanges(ctx, pgconv.UUIDFrom(userID)); err != nil {
		return nil, fmt.Errorf("cancel pending: %w", err)
	}

	if isLoweringOrEqual {
		updated, err := s.q.UpdateUserLimits(ctx, store.UpdateUserLimitsParams{
			ID:              pgconv.UUIDFrom(userID),
			PerTxLimitKobo:  perTxKobo,
			PerDayLimitKobo: perDayKobo,
		})
		if err != nil {
			return nil, fmt.Errorf("apply limits: %w", err)
		}
		return &Outcome{Applied: true, User: &updated}, nil
	}

	pending, err := s.q.CreatePendingLimitChange(ctx, store.CreatePendingLimitChangeParams{
		UserID:          pgconv.UUIDFrom(userID),
		PerTxLimitKobo:  perTxKobo,
		PerDayLimitKobo: perDayKobo,
		AppliesAt:       pgconv.TimeFrom(time.Now().Add(s.cooldown)),
	})
	if err != nil {
		return nil, fmt.Errorf("schedule change: %w", err)
	}
	return &Outcome{Applied: false, User: &user, Pending: &pending}, nil
}

// Pending returns the user's currently-scheduled change, if any.
func (s *LimitsService) Pending(ctx context.Context, userID uuid.UUID) (*store.PendingLimitChange, error) {
	p, err := s.q.GetPendingLimitChange(ctx, pgconv.UUIDFrom(userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// CancelPending cancels the user's scheduled change (if any).
func (s *LimitsService) CancelPending(ctx context.Context, userID uuid.UUID) error {
	return s.q.CancelPendingLimitChanges(ctx, pgconv.UUIDFrom(userID))
}

// ApplyDue is called by the reconciler. It walks every change whose
// applies_at has passed and writes it onto the user record. Idempotent —
// safe to call repeatedly.
func (s *LimitsService) ApplyDue(ctx context.Context, batchSize int32) (int, error) {
	due, err := s.q.ListDuePendingLimits(ctx, store.ListDuePendingLimitsParams{
		AppliesAt: pgconv.TimeFrom(time.Now()),
		Limit:     batchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("list due: %w", err)
	}
	count := 0
	for _, p := range due {
		_, err := s.q.UpdateUserLimits(ctx, store.UpdateUserLimitsParams{
			ID:              p.UserID,
			PerTxLimitKobo:  p.PerTxLimitKobo,
			PerDayLimitKobo: p.PerDayLimitKobo,
		})
		if err != nil {
			s.log.Error("apply pending limit", "id", pgconv.UUIDTo(p.ID), "err", err)
			continue
		}
		if err := s.q.MarkPendingLimitApplied(ctx, p.ID); err != nil {
			s.log.Error("mark pending applied", "id", pgconv.UUIDTo(p.ID), "err", err)
			continue
		}
		count++
	}
	return count, nil
}
