package risk

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

var ErrSelfTrust = errors.New("cannot mark yourself as a trusted merchant")

type TrustedService struct {
	q store.Querier
}

func NewTrustedService(q store.Querier) *TrustedService {
	return &TrustedService{q: q}
}

func (s *TrustedService) Upsert(ctx context.Context, userID, merchantID uuid.UUID, perTxCapKobo int64, label string) (store.TrustedMerchant, error) {
	if userID == merchantID {
		return store.TrustedMerchant{}, ErrSelfTrust
	}
	if perTxCapKobo <= 0 {
		return store.TrustedMerchant{}, fmt.Errorf("cap must be > 0")
	}
	var labelPtr *string
	if label != "" {
		labelPtr = &label
	}
	return s.q.UpsertTrustedMerchant(ctx, store.UpsertTrustedMerchantParams{
		UserID:         pgconv.UUIDFrom(userID),
		MerchantUserID: pgconv.UUIDFrom(merchantID),
		PerTxCapKobo:   perTxCapKobo,
		Label:          labelPtr,
	})
}

func (s *TrustedService) List(ctx context.Context, userID uuid.UUID) ([]store.ListTrustedMerchantsRow, error) {
	rows, err := s.q.ListTrustedMerchants(ctx, pgconv.UUIDFrom(userID))
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []store.ListTrustedMerchantsRow{}, nil
	}
	return rows, nil
}

func (s *TrustedService) Delete(ctx context.Context, userID, recordID uuid.UUID) error {
	return s.q.DeleteTrustedMerchant(ctx, store.DeleteTrustedMerchantParams{
		ID:     pgconv.UUIDFrom(recordID),
		UserID: pgconv.UUIDFrom(userID),
	})
}

// EffectiveCap is the per-transaction limit that should apply for a payment
// from `userID` to `merchantID`. Returns `defaultCap` when no trusted-merchant
// record exists; otherwise returns the smaller of `defaultCap` and the
// trusted cap (trusted caps only override *downward* per the design doc).
func (s *TrustedService) EffectiveCap(ctx context.Context, userID, merchantID uuid.UUID, defaultCap int64) int64 {
	trusted, err := s.q.GetTrustedMerchantCap(ctx, store.GetTrustedMerchantCapParams{
		UserID:         pgconv.UUIDFrom(userID),
		MerchantUserID: pgconv.UUIDFrom(merchantID),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Soft-fail: we never want this to block a payment.
		}
		return defaultCap
	}
	if trusted < defaultCap {
		return trusted
	}
	return defaultCap
}
