package tokens

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
)

type Handler struct {
	svc      *Service
	userIDFn func(echo.Context) uuid.UUID
}

func NewHandler(svc *Service, userIDFn func(echo.Context) uuid.UUID) *Handler {
	return &Handler{svc: svc, userIDFn: userIDFn}
}

func (h *Handler) Mount(g *echo.Group) {
	g.POST("/tokens/issue", h.issue)
	g.GET("/tokens/:code/resolve", h.resolve)
}

type issueBody struct {
	AmountKobo int64  `json:"amount_kobo"`
	Note       string `json:"note"`
}

func (h *Handler) issue(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body issueBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.AmountKobo <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "amount_kobo must be > 0")
	}
	tok, err := h.svc.Issue(c.Request().Context(), IssueRequest{
		IssuedByUserID: userID,
		AmountKobo:     body.AmountKobo,
		Note:           body.Note,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":          tok.ID.String(),
		"code":        tok.Code,
		"amount_kobo": tok.AmountKobo,
		"expires_at":  tok.ExpiresAt.Unix(),
	})
}

func (h *Handler) resolve(c echo.Context) error {
	code := c.Param("code")
	if code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "code required")
	}
	res, err := h.svc.Resolve(c.Request().Context(), code)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":          pgconv.UUIDTo(res.Token.ID).String(),
		"code":        res.Token.Code,
		"amount_kobo": res.Token.AmountKobo,
		"note":        res.Token.Note,
		"expires_at":  pgconv.TimeTo(res.Token.ExpiresAt).Unix(),
		"issuer": map[string]any{
			"id":        pgconv.UUIDTo(res.Issuer.ID).String(),
			"full_name": res.Issuer.FullName,
			"phone":     maskPhone(res.Issuer.Phone),
		},
	})
}

func maskPhone(p string) string {
	if len(p) < 4 {
		return p
	}
	return p[:len(p)-4] + "****" // never expose full phone numbers cross-user
}
