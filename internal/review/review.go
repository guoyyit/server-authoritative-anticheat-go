// Package review implements appeal review. A review re-evaluates an operation
// against the rule version that was in effect and produces an explainable
// result; it never rewrites the original verdict record.
package review

import (
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
)

// Result is the outcome of an appeal review.
type Result struct {
	VerdictID        string         `json:"verdict_id"`
	Upheld           bool           `json:"upheld"`
	Borderline       bool           `json:"borderline"`
	OriginalSeverity model.Severity `json:"original_severity"`
	ReviewSeverity   model.Severity `json:"review_severity"`
	Findings         []model.Finding `json:"findings"`
	Basis            string         `json:"basis"`
}

// Review re-evaluates one verdict on a deep copy of the state. It does not
// mutate the verdict store or the authoritative state.
func Review(v *model.Verdict, op *model.Operation, state *model.MatchState, rs *rules.RuleSet, cfg *config.Config) *Result {
	res := &Result{
		VerdictID:        v.VerdictID,
		OriginalSeverity: model.MaxSeverity(v.Findings),
	}
	if op == nil || state == nil || rs == nil || cfg == nil {
		res.Basis = "insufficient material for review"
		return res
	}

	match := cloneMatch(state)
	p := match.Players[op.PlayerID]
	if p == nil {
		res.Basis = "player not found in match state"
		return res
	}

	ctx := &rules.RuleContext{
		Match:      match,
		Player:     p,
		Op:         op,
		ReceivedAt: v.DecidedAt,
		Config:     cfg,
	}
	var findings []model.Finding
	for _, rule := range rs.Rules {
		findings = append(findings, rule.Evaluate(ctx)...)
	}
	res.Findings = findings
	res.ReviewSeverity = model.MaxSeverity(findings)

	originalRejected := v.Kind == model.VerdictRejected
	reviewRejected := model.SeverityRank(res.ReviewSeverity) >= model.SeverityRank(cfg.RejectThreshold)
	if originalRejected == reviewRejected {
		res.Upheld = true
		res.Basis = "re-evaluation under the bound rule version reproduces the same conclusion"
	} else {
		res.Upheld = false
		res.Basis = "re-evaluation reaches a different conclusion; escalate for manual inspection"
	}
	// Borderline cases sit at the lowest severity band and are classified
	// explicitly rather than silently passed or banned.
	res.Borderline = res.OriginalSeverity == model.SeverityWarning || res.ReviewSeverity == model.SeverityWarning
	return res
}

func cloneMatch(s *model.MatchState) *model.MatchState {
	out := &model.MatchState{
		MatchID:     s.MatchID,
		RuleVersion: s.RuleVersion,
		Version:     s.Version,
		Players:     make(map[string]*model.PlayerState, len(s.Players)),
	}
	for id, p := range s.Players {
		cp := *p
		cp.AppliedSeqs = nil
		out.Players[id] = &cp
	}
	return out
}
