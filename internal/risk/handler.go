package risk

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type Handler struct {
	limits   *LimitsService
	trusted  *TrustedService
	userIDFn func(echo.Context) uuid.UUID
}

func NewHandler(limits *LimitsService, trusted *TrustedService, userIDFn func(echo.Context) uuid.UUID) *Handler {
	return &Handler{limits: limits, trusted: trusted, userIDFn: userIDFn}
}

func (h *Handler) Mount(g *echo.Group) {
	g.GET("/me/limits/pending", h.getPendingLimit)
	g.DELETE("/me/limits/pending", h.cancelPendingLimit)

	g.GET("/trusted-merchants", h.listTrusted)
	g.POST("/trusted-merchants", h.upsertTrusted)
	g.DELETE("/trusted-merchants/:id", h.deleteTrusted)
}

func (h *Handler) getPendingLimit(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	p, err := h.limits.Pending(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if p == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, pendingLimitToJSON(*p))
}

func (h *Handler) cancelPendingLimit(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	if err := h.limits.CancelPending(c.Request().Context(), userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

type upsertTrustedBody struct {
	MerchantUserID string `json:"merchant_user_id"`
	PerTxCapKobo   int64  `json:"per_tx_cap_kobo"`
	Label          string `json:"label"`
}

func (h *Handler) upsertTrusted(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body upsertTrustedBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	merchantID, err := uuid.Parse(body.MerchantUserID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid merchant_user_id")
	}
	if body.PerTxCapKobo <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "per_tx_cap_kobo must be > 0")
	}
	rec, err := h.trusted.Upsert(c.Request().Context(), userID, merchantID, body.PerTxCapKobo, body.Label)
	if err != nil {
		if errors.Is(err, ErrSelfTrust) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, trustedToJSON(rec))
}

func (h *Handler) listTrusted(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	rows, err := h.trusted.List(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, trustedRowToJSON(r))
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) deleteTrusted(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	recordID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := h.trusted.Delete(c.Request().Context(), userID, recordID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func pendingLimitToJSON(p store.PendingLimitChange) map[string]any {
	return map[string]any{
		"id":                 pgconv.UUIDTo(p.ID).String(),
		"per_tx_limit_kobo":  p.PerTxLimitKobo,
		"per_day_limit_kobo": p.PerDayLimitKobo,
		"applies_at":         pgconv.TimeTo(p.AppliesAt).Unix(),
		"created_at":         pgconv.TimeTo(p.CreatedAt),
	}
}

func trustedToJSON(t store.TrustedMerchant) map[string]any {
	return map[string]any{
		"id":               pgconv.UUIDTo(t.ID).String(),
		"merchant_user_id": pgconv.UUIDTo(t.MerchantUserID).String(),
		"per_tx_cap_kobo":  t.PerTxCapKobo,
		"label":            t.Label,
		"created_at":       pgconv.TimeTo(t.CreatedAt),
	}
}

func trustedRowToJSON(r store.ListTrustedMerchantsRow) map[string]any {
	return map[string]any{
		"id":               pgconv.UUIDTo(r.ID).String(),
		"merchant_user_id": pgconv.UUIDTo(r.MerchantUserID).String(),
		"merchant_name":    r.MerchantName,
		"merchant_phone":   maskPhone(r.MerchantPhone),
		"per_tx_cap_kobo":  r.PerTxCapKobo,
		"label":            r.Label,
		"created_at":       pgconv.TimeTo(r.CreatedAt),
	}
}

func maskPhone(p string) string {
	if len(p) < 4 {
		return p
	}
	return p[:len(p)-4] + "****"
}
