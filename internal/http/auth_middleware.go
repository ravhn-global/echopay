package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/auth"
	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

const (
	userContextKey = "echo.user_id"
	jtiContextKey  = "echo.jti"
)

// Re-touch the device_session row at most this often. Reduces write
// amplification on the DB compared to updating last_seen on every request.
const touchAfter = 5 * time.Minute

// AuthMiddleware extracts the Bearer JWT, parses it, verifies the JTI is
// still an active device session, and stores user ID + jti on the context.
// Revoking a device session invalidates its JWT on the very next request.
func AuthMiddleware(issuer *auth.Issuer, q store.Querier) echo.MiddlewareFunc {
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

			session, err := q.GetActiveDeviceSessionByJTI(c.Request().Context(), pgconv.UUIDFrom(claims.JTI))
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return echo.NewHTTPError(http.StatusUnauthorized, "session revoked")
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "session lookup failed")
			}

			// Best-effort last_seen update; the WHERE clause limits writes.
			_ = q.TouchDeviceSession(c.Request().Context(), store.TouchDeviceSessionParams{
				Jti:        pgconv.UUIDFrom(claims.JTI),
				LastSeenAt: pgconv.TimeFrom(time.Now().Add(-touchAfter)),
			})

			c.Set(userContextKey, claims.UserID)
			c.Set(jtiContextKey, claims.JTI)
			// Use session here so the var isn't reported unused if we
			// later branch on its fields.
			_ = session
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

// JTIFrom reads the current request's session id. Used by /me/devices/me
// so a user can identify "this device" in the list.
func JTIFrom(c echo.Context) uuid.UUID {
	v, ok := c.Get(jtiContextKey).(uuid.UUID)
	if !ok {
		return uuid.Nil
	}
	return v
}
