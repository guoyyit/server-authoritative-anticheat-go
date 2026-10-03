package rules

import (
	"fmt"
	"math"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// AttackRateRule enforces the attack rate with a token bucket. The burst
// allowance absorbs jitter (a few attacks landing close together); only
// sustained over-rate fire is flagged.
type AttackRateRule struct{}

// ID implements Rule.
func (AttackRateRule) ID() string { return "attack_rate" }

// Evaluate implements Rule.
func (AttackRateRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	p := ctx.Player
	cfg := ctx.Config
	if op.Kind != model.OpAttack {
		return nil
	}
	dt := 0.0
	if p.LastAttackTime != 0 {
		dt = float64(op.ClientTime-p.LastAttackTime) / 1000.0
	}
	tokens := math.Min(float64(cfg.AttackBurst), p.AttackTokens+dt*cfg.MaxAttackRate)
	if tokens >= 1.0 {
		return nil
	}
	severity := model.SeverityWarning
	if p.AttackViolations >= 2 {
		severity = model.SeverityMinor
	}
	return []model.Finding{{
		RuleID:   "attack_rate",
		Title:    "attack rate exceeds cap",
		Observed: fmt.Sprintf("tokens=%.2f (burst=%d)", tokens, cfg.AttackBurst),
		Allowed:  fmt.Sprintf("%.1f attacks/s with burst %d", cfg.MaxAttackRate, cfg.AttackBurst),
		Severity: severity,
		Basis:    "token bucket refills at the sustained rate and allows the configured burst; sustained over-rate fire is flagged",
	}}
}

// ResourceRule validates that an attack can be paid for from the
// server-authoritative resource pool. Regeneration is projected locally so
// the rule stays a pure function.
type ResourceRule struct{}

// ID implements Rule.
func (ResourceRule) ID() string { return "resource" }

// Evaluate implements Rule.
func (ResourceRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	p := ctx.Player
	cfg := ctx.Config
	if op.Kind != model.OpAttack {
		return nil
	}
	projected := p.Resource
	if p.LastRegenTime != 0 {
		dt := float64(op.ClientTime-p.LastRegenTime) / 1000.0
		if dt > 0 {
			projected = math.Min(cfg.ResourceMax, projected+dt*cfg.ResourceRegen)
		}
	}
	if projected >= cfg.AttackCost {
		return nil
	}
	return []model.Finding{{
		RuleID:   "resource",
		Title:    "insufficient resource for attack",
		Observed: fmt.Sprintf("resource=%.2f cost=%.2f", projected, cfg.AttackCost),
		Allowed:  fmt.Sprintf("resource >= %.2f", cfg.AttackCost),
		Severity: model.SeverityMajor,
		Basis:    "attacks spend server-authoritative resource; a client cannot spend more than it has",
	}}
}

// DamageRule validates reported damage against the per-hit cap and the
// target's remaining health (over-kill).
type DamageRule struct{}

// ID implements Rule.
func (DamageRule) ID() string { return "damage" }

// Evaluate implements Rule.
func (DamageRule) Evaluate(ctx *RuleContext) []model.Finding {
	op := ctx.Op
	cfg := ctx.Config
	if op.Kind != model.OpAttack {
		return nil
	}
	var out []model.Finding
	if op.Damage > cfg.MaxDamage {
		out = append(out, model.Finding{
			RuleID:   "damage",
			Title:    "damage exceeds per-hit cap",
			Observed: fmt.Sprintf("damage=%.2f", op.Damage),
			Allowed:  fmt.Sprintf("0..%.1f", cfg.MaxDamage),
			Severity: model.SeverityMajor,
			Basis:    "reported damage cannot exceed the configured per-hit cap",
		})
	}
	if target := ctx.Match.Players[op.TargetID]; target != nil && op.Damage > target.Health {
		out = append(out, model.Finding{
			RuleID:   "damage",
			Title:    "damage exceeds target remaining health",
			Observed: fmt.Sprintf("damage=%.2f target_health=%.2f", op.Damage, target.Health),
			Allowed:  fmt.Sprintf("<= %.2f", target.Health),
			Severity: model.SeverityMinor,
			Basis:    "a target cannot take more damage than its remaining health (over-kill)",
		})
	}
	return out
}
