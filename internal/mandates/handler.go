package mandates

import (
	"errors"
	"net/http"

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
	g.POST("/mandates/init", h.init)
	g.POST("/mandates/verify", h.verify)
	g.GET("/mandates", h.list)
	g.POST("/mandates/:id/default", h.setDefault)
	g.DELETE("/mandates/:id", h.revoke)
}

type initBody struct {
	Email string `json:"email"`
}

func (h *Handler) init(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body initBody
	_ = c.Bind(&body)

	res, err := h.svc.InitMandate(c.Request().Context(), userID, body.Email)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{
		"authorization_url": res.AuthorizationURL,
		"reference":         res.Reference,
	})
}

type verifyBody struct {
	Reference string `json:"reference"`
}

func (h *Handler) verify(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body verifyBody
	if err := c.Bind(&body); err != nil || body.Reference == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "reference required")
	}
	mandate, err := h.svc.VerifyMandate(c.Request().Context(), userID, body.Reference)
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorizationNotReusable), errors.Is(err, ErrChargeNotSuccessful):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return c.JSON(http.StatusOK, toJSON(mandate))
}

func (h *Handler) list(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	mandates, err := h.svc.ListMandates(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]map[string]any, 0, len(mandates))
	for _, m := range mandates {
		out = append(out, toJSON(m))
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) setDefault(c echo.Context) error {
	userID := h.userIDFn(c)
	mandateID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid mandate id")
	}
	mandate, err := h.svc.SetDefault(c.Request().Context(), userID, mandateID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, toJSON(mandate))
}

func (h *Handler) revoke(c echo.Context) error {
	userID := h.userIDFn(c)
	mandateID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid mandate id")
	}
	if err := h.svc.Revoke(c.Request().Context(), userID, mandateID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func toJSON(m store.Mandate) map[string]any {
	return map[string]any{
		"id":         pgconv.UUIDTo(m.ID).String(),
		"bank_name":  m.BankName,
		"last4":      m.Last4,
		"channel":    m.Channel,
		"status":     m.Status,
		"is_default": m.IsDefault,
		"created_at": pgconv.TimeTo(m.CreatedAt),
	}
}
