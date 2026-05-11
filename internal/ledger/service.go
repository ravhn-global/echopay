// Package ledger writes append-only double-entry records: every settled
// payment produces one debit (sender) and one credit (receiver), both
// referencing the same payment_id.
package ledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type Service struct {
	q store.Querier
}

func NewService(q store.Querier) *Service {
	return &Service{q: q}
}

type Entry struct {
	PaymentID  uuid.UUID
	UserID     uuid.UUID
	Side       string // "debit" | "credit"
	AmountKobo int64
	Reference  string
	Metadata   map[string]any
}

func (s *Service) Record(ctx context.Context, e Entry) (store.LedgerEntry, error) {
	meta := []byte("{}")
	if e.Metadata != nil {
		buf, err := json.Marshal(e.Metadata)
		if err != nil {
			return store.LedgerEntry{}, fmt.Errorf("marshal metadata: %w", err)
		}
		meta = buf
	}
	return s.q.InsertLedgerEntry(ctx, store.InsertLedgerEntryParams{
		PaymentID:  pgconv.UUIDFrom(e.PaymentID),
		UserID:     pgconv.UUIDFrom(e.UserID),
		Side:       e.Side,
		AmountKobo: e.AmountKobo,
		Reference:  e.Reference,
		Metadata:   meta,
	})
}

// RecordPaymentPair writes the debit + credit pair for a settled payment.
func (s *Service) RecordPaymentPair(ctx context.Context, paymentID, sender, receiver uuid.UUID, amountKobo int64, ref string) error {
	if _, err := s.Record(ctx, Entry{
		PaymentID:  paymentID,
		UserID:     sender,
		Side:       "debit",
		AmountKobo: amountKobo,
		Reference:  ref,
	}); err != nil {
		return fmt.Errorf("record debit: %w", err)
	}
	if _, err := s.Record(ctx, Entry{
		PaymentID:  paymentID,
		UserID:     receiver,
		Side:       "credit",
		AmountKobo: amountKobo,
		Reference:  ref,
	}); err != nil {
		return fmt.Errorf("record credit: %w", err)
	}
	return nil
}
