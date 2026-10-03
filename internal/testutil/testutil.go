// Package testutil provides shared helpers for tests.
package testutil

import (
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/security"
)

// Op builds a signed operation with the given parameters.
func Op(cfg *config.Config, match, player string, seq uint64, t int64, kind model.OpKind) *model.Operation {
	op := &model.Operation{
		MatchID:    match,
		PlayerID:   player,
		Seq:        seq,
		ClientTime: t,
		Kind:       kind,
		Position:   model.Vec3{X: float64(seq), Y: 0, Z: 0},
		Nonce:      seq,
	}
	if kind == model.OpAttack {
		op.TargetID = "p2"
		op.Damage = 10
	}
	op.MAC = security.Sign(op, cfg.MACKey)
	return op
}

// MoveOp builds a signed move to an explicit position.
func MoveOp(cfg *config.Config, match, player string, seq uint64, t int64, pos model.Vec3) *model.Operation {
	op := &model.Operation{
		MatchID:    match,
		PlayerID:   player,
		Seq:        seq,
		ClientTime: t,
		Kind:       model.OpMove,
		Position:   pos,
		Nonce:      seq,
	}
	op.MAC = security.Sign(op, cfg.MACKey)
	return op
}

// AttackOp builds a signed attack with explicit damage and target.
func AttackOp(cfg *config.Config, match, player string, seq uint64, t int64, target string, damage float64) *model.Operation {
	op := &model.Operation{
		MatchID:    match,
		PlayerID:   player,
		Seq:        seq,
		ClientTime: t,
		Kind:       model.OpAttack,
		TargetID:   target,
		Damage:     damage,
		Nonce:      seq,
	}
	op.MAC = security.Sign(op, cfg.MACKey)
	return op
}

// HasReason reports whether a verdict carries a reason.
func HasReason(v *model.Verdict, r model.ReasonCode) bool {
	for _, x := range v.Reasons {
		if x == r {
			return true
		}
	}
	return false
}

// HasRule reports whether a verdict has a finding from a rule.
func HasRule(v *model.Verdict, ruleID string) bool {
	for _, f := range v.Findings {
		if f.RuleID == ruleID {
			return true
		}
	}
	return false
}

// FindingsByRule returns findings for a rule.
func FindingsByRule(v *model.Verdict, ruleID string) []model.Finding {
	var out []model.Finding
	for _, f := range v.Findings {
		if f.RuleID == ruleID {
			out = append(out, f)
		}
	}
	return out
}

// CountByKind counts verdicts by kind.
func CountByKind(verdicts []*model.Verdict, kind model.VerdictKind) int {
	n := 0
	for _, v := range verdicts {
		if v.Kind == kind {
			n++
		}
	}
	return n
}

// TestConfig returns a config suitable for tests: client timestamps are
// synthetic and may lead the server clock, so time allowances are relaxed.
func TestConfig() *config.Config {
	c := config.DefaultConfig()
	c.FutureAllowanceMS = 60000
	c.TimeSkewAllowanceMS = 60000
	c.DivergenceMinServerMS = 1000
	return c
}
