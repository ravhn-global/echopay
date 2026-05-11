package http

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/payments"
	"github.com/ravhn/echoapp-backend/internal/paystack"
)

type webhookHandler struct {
	paymentsSvc *payments.Service
	webhookKey  string
	devMode     bool
	log         *slog.Logger
}

func (h *webhookHandler) mount(e *echo.Echo) {
	e.POST("/webhooks/paystack", h.paystack)
}

func (h *webhookHandler) paystack(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "could not read body")
	}
	defer c.Request().Body.Close()

	sig := c.Request().Header.Get("x-paystack-signature")
	if h.webhookKey != "" {
		if !paystack.VerifyWebhookSignature(h.webhookKey, sig, body) {
			h.log.Warn("paystack webhook signature mismatch", "sig", sig)
			return echo.NewHTTPError(http.StatusUnauthorized, "bad signature")
		}
	} else if !h.devMode {
		// Refuse to accept unsigned webhooks in non-dev environments.
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook key not configured")
	}

	ev, err := paystack.ParseWebhookEvent(body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.paymentsSvc.HandleWebhookEvent(c.Request().Context(), ev); err != nil {
		h.log.Error("webhook handler failed", "event", ev.Event, "err", err)
		// Still return 200 so Paystack stops retrying a poisoned event.
	}
	return c.NoContent(http.StatusOK)
}
