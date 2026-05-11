package http

import (
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"
)

func slogRequestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)

			req := c.Request()
			res := c.Response()

			attrs := []any{
				"method", req.Method,
				"path", req.URL.Path,
				"status", res.Status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", res.Header().Get(echo.HeaderXRequestID),
			}
			if err != nil {
				attrs = append(attrs, "err", err.Error())
				log.Error("request", attrs...)
			} else {
				log.Info("request", attrs...)
			}
			return err
		}
	}
}
