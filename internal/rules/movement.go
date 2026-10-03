package rules

import (
	"fmt"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// SpeedRule checks move speed against the authoritative cap. The tolerance
// band absorbs normal network jitter; severity escalates with the observed
// ratio and with sustained violations so a single latency spike is never
// treated as proven cheating.
type SpeedRule struct{}

// ID implements Rule.
func (SpeedRule) ID() string { return "speed" }

// Evaluate implements Rule.
func (SpeedRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	p := ctx.Player
	cfg := ctx.Config
	if op.Kind != model.OpMove || p.LastMoveTime == 0 {
		return nil
	}
	clientDt := float64(op.ClientTime-p.LastMoveTime) / 1000.0
	if clientDt <= 0 {
		return nil // time rule handles non-monotonic timestamps
	}
	dist := op.Position.Distance(p.Position)
	speed := dist / clientDt
	ratio := speed / cfg.MaxSpeed

	if ratio <= 1.0+cfg.SpeedTolerance {
		return nil // within jitter band: no finding
	}

	severity := model.SeverityWarning
	switch {
	case ratio > cfg.SpeedMajorRatio:
		severity = model.SeverityCritical
	case ratio > cfg.SpeedMinorRatio:
		severity = model.SeverityMajor
	case ratio > cfg.SpeedWarnRatio:
		severity = model.SeverityMinor
	}
	// Sustained violations escalate; a single borderline spike stays low.
	if p.SpeedViolations >= 2 && model.SeverityRank(severity) < model.SeverityRank(model.SeverityMajor) {
		severity = model.SeverityMajor
	}

	return []model.Finding{{
		RuleID:   "speed",
		Title:    "move speed exceeds cap",
		Observed: fmt.Sprintf("speed=%.2f m/s ratio=%.2f distance=%.2fm dt=%.0fms", speed, ratio, dist, clientDt*1000),
		Allowed:  fmt.Sprintf("max=%.1f m/s (tolerance %.0f%%, jitter band <= %.2f)", cfg.MaxSpeed, cfg.SpeedTolerance*100, cfg.MaxSpeed*(1+cfg.SpeedTolerance)),
		Severity: severity,
		Basis:    "movement speed is authoritative; ratio beyond the tolerance band with escalation on repeated violations indicates a speed hack",
	}}
}

// TeleportRule detects an instant position displacement beyond what the
// speed cap could explain even over a long interval.
type TeleportRule struct{}

// ID implements Rule.
func (TeleportRule) ID() string { return "teleport" }

// Evaluate implements Rule.
func (TeleportRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	p := ctx.Player
	cfg := ctx.Config
	if op.Kind != model.OpMove || p.LastMoveTime == 0 {
		return nil
	}
	dist := op.Position.Distance(p.Position)
	if dist <= cfg.MaxTeleportDistance {
		return nil
	}
	return []model.Finding{{
		RuleID:   "teleport",
		Title:    "instant displacement beyond teleport cap",
		Observed: fmt.Sprintf("distance=%.2fm", dist),
		Allowed:  fmt.Sprintf("<= %.1fm per operation", cfg.MaxTeleportDistance),
		Severity: model.SeverityCritical,
		Basis:    "a single move cannot displace beyond the teleport cap regardless of network jitter",
	}}
}
