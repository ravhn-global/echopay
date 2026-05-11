package http

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/config"
)

type appVersionHandler struct {
	cfg *config.Config
}

func (h *appVersionHandler) mount(e *echo.Echo) {
	// Public — clients call this before signing in.
	e.GET("/app-version", h.get)
}

func (h *appVersionHandler) get(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"min_supported_version": h.cfg.MinSupportedAppVersion,
		"latest_version":        h.cfg.LatestAppVersion,
	})
}
