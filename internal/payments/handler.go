package payments

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type Handler struct {
	svc      *Service
	userIDFn func(echo.Context) uuid.UUID
}

func NewHandler(svc *Service, userIDFn func(echo.Context) uuid.UUID) *Handler {
	return &Handler{svc: svc, userIDFn: userIDFn}
}

func (h *Handler) Mount(g *echo.Group) {
	g.POST("/payments", h.create)
	g.GET("/payments/:id", h.get)
	g.POST("/payments/:id/refund", h.refund)
	g.POST("/payments/:id/undo", h.undo)
	g.GET("/activity", h.activity)
}

type createBody struct {
	TokenCode      string `json:"token_code"`
	MandateID      string `json:"mandate_id"`
	IdempotencyKey string `json:"idempotency_key"`
	// Optional — when present and > 0, server holds the transfer for this
	// many seconds and exposes hold_expires_at on the response. The client
	// uses its own threshold to decide whether to request a hold; the
	// server doesn't enforce the threshold (clients differ in their
	// configurable prefs).
	UndoWindowSeconds int `json:"undo_window_seconds"`
}

func (h *Handler) create(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body createBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.TokenCode == "" || body.MandateID == "" || body.IdempotencyKey == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "token_code, mandate_id, idempotency_key required")
	}
	mandateID, err := uuid.Parse(body.MandateID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid mandate_id")
	}

	var window time.Duration
	if body.UndoWindowSeconds > 0 && body.UndoWindowSeconds <= 60 {
		window = time.Duration(body.UndoWindowSeconds) * time.Second
	}
	res, err := h.svc.Create(c.Request().Context(), CreateRequest{
		SenderUserID:   userID,
		TokenCode:      body.TokenCode,
		MandateID:      mandateID,
		IdempotencyKey: body.IdempotencyKey,
		UndoWindow:     window,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrTxLimitExceeded), errors.Is(err, ErrDailyLimitExceeded):
			return echo.NewHTTPError(http.StatusForbidden, err.Error())
		case errors.Is(err, ErrMandateNotActive), errors.Is(err, ErrMandateNotOwned):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrReceiverNoBank):
			return echo.NewHTTPError(http.StatusFailedDependency, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return c.JSON(http.StatusCreated, toJSON(res.Payment))
}

func (h *Handler) get(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid payment id")
	}
	p, err := h.svc.q.GetPaymentByID(c.Request().Context(), pgconv.UUIDFrom(id))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if pgconv.UUIDTo(p.SenderUserID) != userID && pgconv.UUIDTo(p.ReceiverUserID) != userID {
		return echo.NewHTTPError(http.StatusForbidden, "not your payment")
	}
	return c.JSON(http.StatusOK, toJSON(p))
}

type refundBody struct {
	MandateID      string `json:"mandate_id"`
	AmountKobo     int64  `json:"amount_kobo"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) refund(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	originalID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid payment id")
	}
	var body refundBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.MandateID == "" || body.IdempotencyKey == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "mandate_id and idempotency_key required")
	}
	mandateID, err := uuid.Parse(body.MandateID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid mandate_id")
	}
	res, err := h.svc.Refund(c.Request().Context(), RefundRequest{
		OriginalPaymentID: originalID,
		RefunderUserID:    userID,
		MandateID:         mandateID,
		AmountKobo:        body.AmountKobo,
		IdempotencyKey:    body.IdempotencyKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotReceiver), errors.Is(err, ErrAlreadyRefunded):
			return echo.NewHTTPError(http.StatusForbidden, err.Error())
		case errors.Is(err, ErrRefundTooLarge), errors.Is(err, ErrOriginalNotSettled),
			errors.Is(err, ErrMandateNotActive), errors.Is(err, ErrMandateNotOwned):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrTxLimitExceeded), errors.Is(err, ErrDailyLimitExceeded):
			return echo.NewHTTPError(http.StatusForbidden, err.Error())
		case errors.Is(err, ErrReceiverNoBank):
			return echo.NewHTTPError(http.StatusFailedDependency, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return c.JSON(http.StatusCreated, toJSON(res.Payment))
}

func (h *Handler) undo(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid payment id")
	}
	updated, err := h.svc.Undo(c.Request().Context(), UndoRequest{
		PaymentID: id,
		CallerID:  userID,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotSender):
			return echo.NewHTTPError(http.StatusForbidden, err.Error())
		case errors.Is(err, ErrUndoWindowClosed):
			return echo.NewHTTPError(http.StatusGone, err.Error())
		case errors.Is(err, ErrUndoUnavailable):
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return c.JSON(http.StatusOK, toJSON(updated))
}

func (h *Handler) activity(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	rows, err := h.svc.ListUserActivity(c.Request().Context(), userID, int32(limit), int32(offset))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		row := toJSON(p)
		row["direction"] = "received"
		if pgconv.UUIDTo(p.SenderUserID) == userID {
			row["direction"] = "sent"
		}
		out = append(out, row)
	}
	return c.JSON(http.StatusOK, out)
}

func toJSON(p store.Payment) map[string]any {
	out := map[string]any{
		"id":               pgconv.UUIDTo(p.ID).String(),
		"token_id":         pgconv.UUIDTo(p.TokenID).String(),
		"sender_user_id":   pgconv.UUIDTo(p.SenderUserID).String(),
		"receiver_user_id": pgconv.UUIDTo(p.ReceiverUserID).String(),
		"amount_kobo":      p.AmountKobo,
		"status":           p.Status,
		"charge_status":    p.ChargeStatus,
		"transfer_status":  p.TransferStatus,
		"failure_reason":   p.FailureReason,
		"created_at":       pgconv.TimeTo(p.CreatedAt),
		"settled_at":       pgconv.TimeTo(p.SettledAt),
	}
	if p.HoldExpiresAt.Valid {
		out["hold_expires_at"] = pgconv.TimeTo(p.HoldExpiresAt).Unix()
	}
	return out
}
