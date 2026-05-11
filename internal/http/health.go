package http

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

type healthStatus struct {
	Status   string            `json:"status"`
	Services map[string]string `json:"services"`
}

func registerHealth(e *echo.Echo, pool *pgxpool.Pool, rdb *redis.Client) {
	e.GET("/health", func(c echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()

		services := map[string]string{}
		overall := "ok"

		if err := pool.Ping(ctx); err != nil {
			services["postgres"] = "down: " + err.Error()
			overall = "degraded"
		} else {
			services["postgres"] = "ok"
		}

		if err := rdb.Ping(ctx).Err(); err != nil {
			services["redis"] = "down: " + err.Error()
			overall = "degraded"
		} else {
			services["redis"] = "ok"
		}

		status := http.StatusOK
		if overall != "ok" {
			status = http.StatusServiceUnavailable
		}
		return c.JSON(status, healthStatus{Status: overall, Services: services})
	})

	e.GET("/", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"service": "echoapp-backend",
			"version": "0.0.1",
		})
	})
}
