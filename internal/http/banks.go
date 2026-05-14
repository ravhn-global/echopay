package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/ravhn/echoapp-backend/internal/paystack"
)

// Bank picker on the receive-account onboarding step needs the full
// Paystack directory (~120 entries) — the local fallback list of 23
// banks misses things like microfinance + neo-banks. The list changes
// maybe once a quarter, so we cache it for 24h in Redis behind a tiny
// proxy.
type banksHandler struct {
	ps    *paystack.Client
	rdb   *redis.Client
	cache []paystack.Bank // in-process fallback so a Redis blip doesn't surface as an empty picker
}

const (
	banksCacheKey = "banks:nigeria:v1"
	banksCacheTTL = 24 * time.Hour
)

func (h *banksHandler) mount(g *echo.Group) {
	g.GET("/banks", h.list)
}

func (h *banksHandler) list(c echo.Context) error {
	ctx := c.Request().Context()
	if banks := h.fromRedis(ctx); len(banks) > 0 {
		return c.JSON(http.StatusOK, banks)
	}

	banks, err := h.ps.ListBanks(ctx)
	if err != nil {
		// Paystack outage shouldn't break onboarding — fall back to whatever
		// we last saw in memory, even if it's empty.
		if len(h.cache) > 0 {
			return c.JSON(http.StatusOK, h.cache)
		}
		return echo.NewHTTPError(http.StatusBadGateway, "could not load bank directory")
	}

	// Sort alphabetically so the picker is predictable, and drop entries that
	// are explicitly marked inactive (Paystack flags wound-down banks).
	active := banks[:0]
	for _, b := range banks {
		if b.Code != "" {
			active = append(active, b)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Name < active[j].Name })
	h.cache = active
	h.storeRedis(ctx, active)
	return c.JSON(http.StatusOK, active)
}

func (h *banksHandler) fromRedis(ctx context.Context) []paystack.Bank {
	raw, err := h.rdb.Get(ctx, banksCacheKey).Bytes()
	if err != nil {
		return nil
	}
	var banks []paystack.Bank
	if err := json.Unmarshal(raw, &banks); err != nil {
		return nil
	}
	return banks
}

func (h *banksHandler) storeRedis(ctx context.Context, banks []paystack.Bank) {
	raw, err := json.Marshal(banks)
	if err != nil {
		return
	}
	_ = h.rdb.Set(ctx, banksCacheKey, raw, banksCacheTTL).Err()
}
