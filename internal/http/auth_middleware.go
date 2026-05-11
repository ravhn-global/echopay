package http

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/auth"
)

const userContextKey = "echo.user_id"

// AuthMiddleware extracts the Bearer JWT, parses it, and stores the user ID
// in the echo context. Use UserIDFrom(c) to read it in handlers.
func AuthMiddleware(issuer *auth.Issuer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Request().Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing bearer token")
			}
			token := strings.TrimPrefix(h, "Bearer ")
			claims, err := issuer.Parse(token)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
			}
			c.Set(userContextKey, claims.UserID)
			return next(c)
		}
	}
}

// UserIDFrom reads the authenticated user ID stored by AuthMiddleware.
// Returns uuid.Nil if missing (caller should reject in that case).
func UserIDFrom(c echo.Context) uuid.UUID {
	v, ok := c.Get(userContextKey).(uuid.UUID)
	if !ok {
		return uuid.Nil
	}
	return v
}
