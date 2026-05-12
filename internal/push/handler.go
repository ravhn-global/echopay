package push

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type Handler struct {
	q        store.Querier
	userIDFn func(echo.Context) uuid.UUID
	jtiFn    func(echo.Context) uuid.UUID
}

func NewHandler(q store.Querier, userIDFn, jtiFn func(echo.Context) uuid.UUID) *Handler {
	return &Handler{q: q, userIDFn: userIDFn, jtiFn: jtiFn}
}

func (h *Handler) Mount(g *echo.Group) {
	g.POST("/me/push-token", h.register)
	g.DELETE("/me/push-token", h.revoke)
}

type registerBody struct {
	FcmToken string `json:"fcm_token"`
	Platform string `json:"platform"` // "ios" | "android"
}

func (h *Handler) register(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body registerBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.FcmToken == "" || (body.Platform != "ios" && body.Platform != "android") {
		return echo.NewHTTPError(http.StatusBadRequest, "fcm_token and platform required")
	}

	// Bind the token to the current device session so revoking the session
	// cascades to its push tokens too.
	var sessionID pgtype.UUID
	if jti := h.jtiFn(c); jti != uuid.Nil {
		if s, err := h.q.GetActiveDeviceSessionByJTI(
			c.Request().Context(), pgconv.UUIDFrom(jti),
		); err == nil {
			sessionID = s.ID
		}
	}

	if _, err := h.q.UpsertPushToken(c.Request().Context(), store.UpsertPushTokenParams{
		UserID:          pgconv.UUIDFrom(userID),
		DeviceSessionID: sessionID,
		FcmToken:        body.FcmToken,
		Platform:        body.Platform,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

type revokeBody struct {
	FcmToken string `json:"fcm_token"`
}

func (h *Handler) revoke(c echo.Context) error {
	var body revokeBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.FcmToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "fcm_token required")
	}
	if err := h.q.RevokePushToken(c.Request().Context(), body.FcmToken); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
