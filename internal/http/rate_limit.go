package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// RateLimit is a fixed-window counter per (subject, route) backed by Redis.
// Cheap, monotonic, no Lua. The window is the bucket TTL; the count is the
// INCR result. When count > limit within the window, return 429 with a
// Retry-After header derived from the bucket's remaining TTL.
//
// Subject is whatever identifies a caller — IP, user id, phone, etc.
// Pick what's appropriate for the endpoint:
//   - Pre-auth: use the request IP (echo.RealIP).
//   - Post-auth: use the user id.
type RateLimit struct {
	rdb    *redis.Client
	scope  string        // namespace, e.g. "otp" or "pay"
	limit  int64         // max calls per window
	window time.Duration // window length
}

func NewRateLimit(rdb *redis.Client, scope string, limit int64, window time.Duration) *RateLimit {
	return &RateLimit{rdb: rdb, scope: scope, limit: limit, window: window}
}

// Middleware returns an Echo MiddlewareFunc. `subject` extracts the bucket
// key from the request — caller decides whether that's IP, user id, etc.
func (r *RateLimit) Middleware(subject func(echo.Context) string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			s := subject(c)
			if s == "" {
				// No subject means we can't bucket — fail open. Better
				// to let one unbucketable request through than to deny
				// every healthcheck.
				return next(c)
			}
			key := "rl:" + r.scope + ":" + s
			ctx := c.Request().Context()

			count, err := r.rdb.Incr(ctx, key).Result()
			if err != nil {
				// Redis blip — fail open. A rate-limit miss is far less
				// damaging than refusing the user when Redis hiccups.
				return next(c)
			}
			if count == 1 {
				// First hit in this window — set the TTL.
				_ = r.rdb.Expire(ctx, key, r.window).Err()
			}
			if count > r.limit {
				ttl, _ := r.rdb.TTL(ctx, key).Result()
				if ttl > 0 {
					c.Response().Header().Set("Retry-After", strconv.Itoa(int(ttl.Seconds())))
				}
				c.Response().Header().Set("X-RateLimit-Limit", strconv.FormatInt(r.limit, 10))
				c.Response().Header().Set("X-RateLimit-Window", r.window.String())
				return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
			}
			return next(c)
		}
	}
}

// IPSubject returns echo.RealIP — useful for pre-auth buckets like OTP.
func IPSubject(c echo.Context) string {
	return c.RealIP()
}

// UserIDSubject returns the authenticated user id, falling back to IP for
// requests that haven't been through AuthMiddleware yet.
func UserIDSubject(c echo.Context) string {
	id := UserIDFrom(c)
	if id.String() != "00000000-0000-0000-0000-000000000000" {
		return id.String()
	}
	return c.RealIP()
}
