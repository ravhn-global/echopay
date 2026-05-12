package http

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/paystack"
)

// statusHandler exposes the rolling Paystack-health snapshot so the
// Flutter client can surface the design system's degraded banner.
type statusHandler struct {
	ps *paystack.Client
}

func (h *statusHandler) mount(e *echo.Echo) {
	// Public — called by signed-in users on a 60s interval. Auth would just
	// add load without changing the answer.
	e.GET("/v1/status", h.get)
}

func (h *statusHandler) get(c echo.Context) error {
	snap := h.ps.Health.Snapshot()
	resp := map[string]any{
		"status": snap.Status,
		"window": map[string]any{
			"total":    snap.Total,
			"failures": snap.Failures,
			"start":    snap.WindowStart.Unix(),
		},
	}
	switch snap.Status {
	case "degraded":
		resp["message"] = "Banking is slow right now. Payments may take 2–5 minutes to settle. Your money is safe."
	case "outage":
		resp["message"] = "Pay and Receive are paused while we work with our bank partner. We'll be back shortly."
	}
	return c.JSON(http.StatusOK, resp)
}
