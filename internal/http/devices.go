package http

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type devicesHandler struct {
	q store.Querier
}

func (h *devicesHandler) mount(g *echo.Group) {
	g.GET("/me/devices", h.list)
	g.DELETE("/me/devices/:id", h.revoke)
	g.DELETE("/me/devices", h.revokeAllOthers) // keep current, kill the rest
}

func (h *devicesHandler) list(c echo.Context) error {
	userID := UserIDFrom(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	currentJTI := JTIFrom(c)
	sessions, err := h.q.ListUserDeviceSessions(c.Request().Context(), pgconv.UUIDFrom(userID))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionToJSON(s, pgconv.UUIDTo(s.Jti) == currentJTI))
	}
	return c.JSON(http.StatusOK, out)
}

func (h *devicesHandler) revoke(c echo.Context) error {
	userID := UserIDFrom(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid session id")
	}
	if err := h.q.RevokeDeviceSession(c.Request().Context(), store.RevokeDeviceSessionParams{
		ID:     pgconv.UUIDFrom(sessionID),
		UserID: pgconv.UUIDFrom(userID),
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *devicesHandler) revokeAllOthers(c echo.Context) error {
	userID := UserIDFrom(c)
	currentJTI := JTIFrom(c)
	if userID == uuid.Nil || currentJTI == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	// Need the session id, not the jti, to spare it. Pull current session.
	session, err := h.q.GetActiveDeviceSessionByJTI(c.Request().Context(), pgconv.UUIDFrom(currentJTI))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err := h.q.RevokeAllOtherDeviceSessions(c.Request().Context(), store.RevokeAllOtherDeviceSessionsParams{
		UserID: pgconv.UUIDFrom(userID),
		ID:     session.ID,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func sessionToJSON(s store.DeviceSession, isCurrent bool) map[string]any {
	return map[string]any{
		"id":           pgconv.UUIDTo(s.ID).String(),
		"is_current":   isCurrent,
		"model":        s.Model,
		"os_name":      s.OsName,
		"os_version":   s.OsVersion,
		"app_version":  s.AppVersion,
		"last_seen_at": pgconv.TimeTo(s.LastSeenAt),
		"created_at":   pgconv.TimeTo(s.CreatedAt),
	}
}
