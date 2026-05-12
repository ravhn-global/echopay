// Package audit records security-relevant state changes for compliance
// and user-visible transparency. Append-only — no UPDATE / DELETE on the
// table, ever. Every Record call returns immediately with the inserted
// row; callers should generally `_ = audit.Record(...)` because audit
// failures must never block the action that triggered them.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

// Action vocabulary — stable strings so future filters / dashboards stay
// useful as schemas evolve.
const (
	ActionAuthSignin             = "auth.signin"
	ActionAccountLocked          = "auth.account_locked"
	ActionPaymentCreated         = "payment.created"
	ActionPaymentSettled         = "payment.settled"
	ActionPaymentFailed          = "payment.failed"
	ActionPaymentAutoRefunded    = "payment.auto_refunded"
	ActionRefundIssued           = "refund.issued"
	ActionLimitsAppliedNow       = "limits.applied_now"
	ActionLimitsScheduled        = "limits.scheduled"
	ActionLimitsScheduleCancelled = "limits.schedule_cancelled"
)

type Event struct {
	UserID    uuid.UUID
	ActorID   uuid.UUID
	Action    string
	TargetTyp string
	TargetID  string
	Metadata  map[string]any
	IP        string
	UserAgent string
}

type Service struct {
	q   store.Querier
	log *slog.Logger
}

func NewService(q store.Querier, log *slog.Logger) *Service {
	return &Service{q: q, log: log}
}

// Record writes an event. Failures are logged but never surfaced — audit
// must never block the action that triggered it.
func (s *Service) Record(ctx context.Context, e Event) {
	meta := []byte("{}")
	if e.Metadata != nil {
		if buf, err := json.Marshal(e.Metadata); err == nil {
			meta = buf
		}
	}
	params := store.InsertAuditEventParams{
		UserID:   pgconv.UUIDFrom(e.UserID),
		Action:   e.Action,
		Metadata: meta,
	}
	if e.ActorID != uuid.Nil {
		params.ActorUserID = pgconv.UUIDFrom(e.ActorID)
	}
	if e.TargetTyp != "" {
		params.TargetType = &e.TargetTyp
	}
	if e.TargetID != "" {
		params.TargetID = &e.TargetID
	}
	if e.IP != "" {
		params.Ip = &e.IP
	}
	if e.UserAgent != "" {
		params.UserAgent = &e.UserAgent
	}
	if _, err := s.q.InsertAuditEvent(ctx, params); err != nil {
		s.log.Error("audit record failed", "action", e.Action, "err", err)
	}
}

// List paginates a user's audit trail.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit, offset int32) ([]store.AuditEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.q.ListUserAuditEvents(ctx, store.ListUserAuditEventsParams{
		UserID: pgconv.UUIDFrom(userID),
		Limit:  limit,
		Offset: offset,
	})
}
