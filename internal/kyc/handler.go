package kyc

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
)

type Handler struct {
	svc       *Service
	userIDFn  func(echo.Context) uuid.UUID
}

func NewHandler(svc *Service, userIDFn func(echo.Context) uuid.UUID) *Handler {
	return &Handler{svc: svc, userIDFn: userIDFn}
}

func (h *Handler) Mount(g *echo.Group) {
	g.POST("/kyc/bvn", h.submitBVN)
}

type submitBVNBody struct {
	FullName    string `json:"full_name"`
	BVN         string `json:"bvn"`
	DateOfBirth string `json:"date_of_birth"` // YYYY-MM-DD
}

func (h *Handler) submitBVN(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	var body submitBVNBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	dob, err := time.Parse("2006-01-02", body.DateOfBirth)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "date_of_birth must be YYYY-MM-DD")
	}

	user, err := h.svc.SubmitBVN(c.Request().Context(), SubmitBVNRequest{
		UserID:      userID,
		FullName:    body.FullName,
		BVN:         body.BVN,
		DateOfBirth: dob,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidBVN), errors.Is(err, ErrInvalidDOB):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		default:
			return echo.NewHTTPError(http.StatusInternalServerError, "kyc failed")
		}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":         pgconv.UUIDTo(user.ID).String(),
		"kyc_tier":   user.KycTier,
		"kyc_status": user.KycStatus,
		"full_name":  user.FullName,
	})
}
