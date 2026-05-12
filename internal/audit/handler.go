package audit

import (
	"encoding/json"
	"net/http"
	"strconv"

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
	g.GET("/me/audit", h.list)
}

func (h *Handler) list(c echo.Context) error {
	userID := h.userIDFn(c)
	if userID == uuid.Nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "no user")
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))

	rows, err := h.svc.List(c.Request().Context(), userID, int32(limit), int32(offset))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, toJSON(r))
	}
	return c.JSON(http.StatusOK, out)
}

func toJSON(e store.AuditEvent) map[string]any {
	row := map[string]any{
		"id":          e.ID,
		"action":      e.Action,
		"target_type": e.TargetType,
		"target_id":   e.TargetID,
		"ip":          e.Ip,
		"created_at":  pgconv.TimeTo(e.CreatedAt),
	}
	// metadata is stored as JSONB; surface it as a parsed object so the
	// client doesn't have to double-decode.
	if len(e.Metadata) > 0 {
		var m map[string]any
		if json.Unmarshal(e.Metadata, &m) == nil {
			row["metadata"] = m
		}
	}
	return row
}
