package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/auth"
	"github.com/ravhn/echoapp-backend/internal/paystack"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/risk"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type meHandler struct {
	authSvc *auth.Service
	q       store.Querier
	ps      *paystack.Client
	limits  *risk.LimitsService
}

func (h *meHandler) mount(g *echo.Group) {
	g.GET("/me", h.get)
	g.PUT("/me/receive-account", h.setReceiveAccount)
	g.PUT("/me/limits", h.setLimits)
}

func (h *meHandler) get(c echo.Context) error {
	userID := UserIDFrom(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	u, err := h.authSvc.CurrentUser(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":                 pgconv.UUIDTo(u.ID).String(),
		"phone":              u.Phone,
		"full_name":          u.FullName,
		"kyc_tier":           u.KycTier,
		"kyc_status":         u.KycStatus,
		"nuban":              u.Nuban,
		"bank_code":          u.BankCode,
		"account_name":       u.AccountName,
		"per_tx_limit_kobo":  u.PerTxLimitKobo,
		"per_day_limit_kobo": u.PerDayLimitKobo,
		"status":             u.Status,
	})
}

type receiveAccountBody struct {
	NUBAN    string `json:"nuban"`
	BankCode string `json:"bank_code"`
}

func (h *meHandler) setReceiveAccount(c echo.Context) error {
	userID := UserIDFrom(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body receiveAccountBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.NUBAN == "" || body.BankCode == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "nuban and bank_code required")
	}
	resolved, err := h.ps.ResolveAccount(c.Request().Context(), body.NUBAN, body.BankCode)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "could not resolve account at bank")
	}
	u, err := h.q.UpdateUserReceiveAccount(c.Request().Context(), store.UpdateUserReceiveAccountParams{
		ID:          pgconv.UUIDFrom(userID),
		Nuban:       &body.NUBAN,
		BankCode:    &body.BankCode,
		AccountName: &resolved.AccountName,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{
		"nuban":        u.Nuban,
		"bank_code":    u.BankCode,
		"account_name": u.AccountName,
	})
}

type limitsBody struct {
	PerTxLimitKobo  int64 `json:"per_tx_limit_kobo"`
	PerDayLimitKobo int64 `json:"per_day_limit_kobo"`
}

func (h *meHandler) setLimits(c echo.Context) error {
	userID := UserIDFrom(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body limitsBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	outcome, err := h.limits.SetLimits(
		c.Request().Context(), userID, body.PerTxLimitKobo, body.PerDayLimitKobo,
	)
	if err != nil {
		if errors.Is(err, risk.ErrLimitsInvalid) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	resp := map[string]any{
		"applied":            outcome.Applied,
		"per_tx_limit_kobo":  outcome.User.PerTxLimitKobo,
		"per_day_limit_kobo": outcome.User.PerDayLimitKobo,
	}
	if outcome.Pending != nil {
		resp["pending"] = map[string]any{
			"per_tx_limit_kobo":  outcome.Pending.PerTxLimitKobo,
			"per_day_limit_kobo": outcome.Pending.PerDayLimitKobo,
			"applies_at":         pgconv.TimeTo(outcome.Pending.AppliesAt).Unix(),
		}
	}
	return c.JSON(http.StatusOK, resp)
}
