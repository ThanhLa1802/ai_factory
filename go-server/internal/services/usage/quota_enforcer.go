package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/observability"
)

// Quota modes. off disables the gate entirely (the composition root wires a nil
// gate), shadow records breaches without blocking, enforce rejects the request.
const (
	QuotaModeOff     = "off"
	QuotaModeShadow  = "shadow"
	QuotaModeEnforce = "enforce"
)

// Quota kinds. quota_type is matched by prefix so both the compact form
// ("tokens", "requests") and the UI form ("tokens_per_day") work. The period
// column (daily|monthly) — not the type suffix — defines the window.
const (
	QuotaKindTokens   = "tokens"
	QuotaKindRequests = "requests"
)

// ErrQuotaExceeded is returned by QuotaEnforcer.Check in enforce mode when a
// tenant has met or exceeded one of its quotas.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaConfig configures a QuotaEnforcer. Log defaults to slog.Default().
type QuotaConfig struct {
	Mode string
	Log  *slog.Logger
}

// QuotaEnforcer checks a tenant's usage against its tenant_quotas rows. Reads
// come from the usage_daily rollup (lag ≤ one Roller interval), so enforcement
// is approximate at the boundary — the same trade-off as the usage API.
type QuotaEnforcer struct {
	repos Repositories
	mode  string
	log   *slog.Logger
	now   func() time.Time
}

// NewQuotaEnforcer builds an enforcer over the usage repositories.
func NewQuotaEnforcer(repos Repositories, cfg QuotaConfig) *QuotaEnforcer {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &QuotaEnforcer{repos: repos, mode: cfg.Mode, log: log, now: time.Now}
}

// Mode reports the configured mode.
func (e *QuotaEnforcer) Mode() string { return e.mode }

// Check returns ErrQuotaExceeded (wrapped with the quota detail) when the tenant
// is at or over a quota and the mode is enforce. In shadow mode it logs the
// breach and returns nil. Repository errors are returned to the caller, which
// treats them as fail-open.
func (e *QuotaEnforcer) Check(ctx context.Context, tenantID string) error {
	quotas, err := e.repos.Quotas.List(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("list quotas: %w", err)
	}
	if len(quotas) == 0 {
		return nil
	}
	now := e.now()
	for _, q := range quotas {
		kind := quotaKind(q.QuotaType)
		if kind == "" || q.LimitValue <= 0 {
			continue // unknown type (e.g. concurrent_requests) or no limit
		}
		from, to := periodWindow(q.Period, now)
		sum, err := e.repos.Aggregates.Summary(ctx, tenantID, from, to)
		if err != nil {
			return fmt.Errorf("quota usage: %w", err)
		}
		var used int64
		switch kind {
		case QuotaKindTokens:
			used = sum.TotalTokens
		case QuotaKindRequests:
			used = sum.Requests
		}
		if used < q.LimitValue {
			continue
		}

		observability.RecordQuotaExceeded(tenantID, kind, e.mode)
		breach := fmt.Errorf("%w: %s %s limit %d reached (used %d)", ErrQuotaExceeded, kind, q.Period, q.LimitValue, used)
		if e.mode == QuotaModeEnforce {
			return breach
		}
		e.log.Warn("quota exceeded (shadow)", "tenant", tenantID, "quota_type", kind,
			"period", q.Period, "limit", q.LimitValue, "used", used)
	}
	return nil
}

// quotaKind maps a quota_type string to the metric it limits. Unknown types
// (concurrency, gpu_hours, …) return "" and are ignored.
func quotaKind(quotaType string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(quotaType)), "tokens"):
		return QuotaKindTokens
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(quotaType)), "requests"):
		return QuotaKindRequests
	default:
		return ""
	}
}

// periodWindow returns the inclusive [from, to] UTC date window for a quota
// period. daily is the current calendar day, monthly the current calendar month;
// anything else falls back to daily.
func periodWindow(period string, now time.Time) (from, to time.Time) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "monthly", "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now
	default:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), now
	}
}
