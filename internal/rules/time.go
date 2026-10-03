package rules

import (
	"fmt"
	"math"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// TimeRule validates client timestamps against server receive time. It
// detects forged clocks: non-monotonic timestamps, timestamps in the future,
// and excessive clock skew.
type TimeRule struct{}

// ID implements Rule.
func (TimeRule) ID() string { return "time_skew" }

// Evaluate implements Rule.
func (TimeRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	p := ctx.Player
	cfg := ctx.Config
	var out []model.Finding

	if p.LastOpTime != 0 {
		if op.ClientTime <= p.LastOpTime {
			out = append(out, model.Finding{
				RuleID:   "time_skew",
				Title:    "client time not monotonic",
				Observed: fmt.Sprintf("client_time=%d last=%d", op.ClientTime, p.LastOpTime),
				Allowed:  "strictly increasing",
				Severity: model.SeverityCritical,
				Basis:    "client timestamps must be strictly increasing; a rollback indicates a forged clock",
			})
		}
		clientDt := float64(op.ClientTime - p.LastOpTime)
		serverDt := float64(ctx.ReceivedAt - p.LastServerTime)
		// Only evaluate divergence over a meaningful wall-clock interval;
		// sub-second server intervals are dominated by bursty network jitter
		// and the ratio is meaningless there.
		if clientDt > 0 && serverDt >= float64(cfg.DivergenceMinServerMS) {
			ratio := math.Max(clientDt, serverDt) / math.Min(clientDt, serverDt)
			if ratio > cfg.TimeDivergenceFactor {
				out = append(out, model.Finding{
					RuleID:   "time_skew",
					Title:    "client/server time divergence",
					Observed: fmt.Sprintf("client_dt=%.0fms server_dt=%.0fms ratio=%.2f", clientDt, serverDt, ratio),
					Allowed:  fmt.Sprintf("ratio <= %.1f", cfg.TimeDivergenceFactor),
					Severity: model.SeverityCritical,
					Basis:    "client and server elapsed time must agree within the divergence factor",
				})
			}
		}
	}

	skew := math.Abs(float64(op.ClientTime - ctx.ReceivedAt))
	if skew > float64(cfg.TimeSkewAllowanceMS) {
		out = append(out, model.Finding{
			RuleID:   "time_skew",
			Title:    "excessive clock skew",
			Observed: fmt.Sprintf("skew=%.0fms", skew),
			Allowed:  fmt.Sprintf("|skew| <= %dms", cfg.TimeSkewAllowanceMS),
			Severity: model.SeverityCritical,
			Basis:    "client clock deviates from server time beyond the configured skew allowance",
		})
	}
	if op.ClientTime > ctx.ReceivedAt+cfg.FutureAllowanceMS {
		out = append(out, model.Finding{
			RuleID:   "time_skew",
			Title:    "future timestamp",
			Observed: fmt.Sprintf("client_time=%d server_time=%d", op.ClientTime, ctx.ReceivedAt),
			Allowed:  fmt.Sprintf("client <= server+%dms", cfg.FutureAllowanceMS),
			Severity: model.SeverityCritical,
			Basis:    "client timestamps must not be in the future",
		})
	}
	return out
}
