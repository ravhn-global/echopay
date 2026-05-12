package paystack

import (
	"sync"
	"time"
)

// Health tracks recent Paystack call outcomes so the server can surface a
// "service degraded" banner when the rail is wobbling. In-process ring
// buffer — single-instance deploys see accurate numbers; for multi-instance
// you'd swap to Redis sorted sets and aggregate across pods.
type Health struct {
	mu      sync.Mutex
	outcomes []outcome // newest at the end
	maxAge  time.Duration
	cap     int
}

type outcome struct {
	at      time.Time
	success bool
}

func NewHealth(maxAge time.Duration, capHint int) *Health {
	if capHint <= 0 {
		capHint = 256
	}
	return &Health{maxAge: maxAge, cap: capHint}
}

func (h *Health) record(success bool) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outcomes = append(h.outcomes, outcome{at: now, success: success})
	cutoff := now.Add(-h.maxAge)
	// Drop entries beyond maxAge or cap. Linear scan is fine at this size.
	i := 0
	for ; i < len(h.outcomes); i++ {
		if h.outcomes[i].at.After(cutoff) {
			break
		}
	}
	if i > 0 {
		h.outcomes = h.outcomes[i:]
	}
	if len(h.outcomes) > h.cap {
		h.outcomes = h.outcomes[len(h.outcomes)-h.cap:]
	}
}

// Snapshot reports the current health. Status is:
//   - "outage"   ≥50% failures over the last maxAge AND ≥4 samples
//   - "degraded" ≥20% failures AND ≥4 samples
//   - "ok"       otherwise (or not enough samples — failing-open is the
//                right default; we don't want a quiet morning to look
//                like an outage)
type Snapshot struct {
	Status      string
	Total       int
	Failures    int
	WindowStart time.Time
}

func (h *Health) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := time.Now().Add(-h.maxAge)
	total, fails := 0, 0
	for _, o := range h.outcomes {
		if !o.at.After(cutoff) {
			continue
		}
		total++
		if !o.success {
			fails++
		}
	}
	status := "ok"
	if total >= 4 {
		ratio := float64(fails) / float64(total)
		switch {
		case ratio >= 0.5:
			status = "outage"
		case ratio >= 0.2:
			status = "degraded"
		}
	}
	return Snapshot{
		Status:      status,
		Total:       total,
		Failures:    fails,
		WindowStart: cutoff,
	}
}
