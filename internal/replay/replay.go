// Package replay records the ordered stream of operations a match received
// (with server receive timestamps) and replays it deterministically against a
// rule version. Replay conclusions must be byte-identical to online ones.
package replay

import (
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
)

// Entry is one recorded operation with its server receive time and, for
// submit-boundary rejections, the rejection reason.
type Entry struct {
	Op             *model.Operation `json:"op"`
	ReceivedAt     int64            `json:"received_at"`
	RejectedReason model.ReasonCode `json:"rejected_reason,omitempty"`
}

// Log is the full replay record of a match.
type Log struct {
	MatchID     string  `json:"match_id"`
	RuleVersion string  `json:"rule_version"`
	Entries     []Entry `json:"entries"`
}

// FromEngine builds a replay log from a match engine's recorded stream.
func FromEngine(matchID, ruleVersion string, ops []engine.RecordedOp) *Log {
	log := &Log{MatchID: matchID, RuleVersion: ruleVersion, Entries: make([]Entry, 0, len(ops))}
	for _, op := range ops {
		log.Entries = append(log.Entries, Entry{
			Op:             op.Op,
			ReceivedAt:     op.ReceivedAt,
			RejectedReason: op.RejectedReason,
		})
	}
	return log
}

// Replay feeds the recorded entries in arrival order through a fresh engine
// bound to the recorded rule version and returns the resulting verdicts.
// Semantic operations are re-evaluated through the same deterministic path;
// submit-boundary rejections are reproduced from the recorded reason.
func Replay(reg *rules.Registry, log *Log, cfg *config.Config) ([]*model.Verdict, error) {
	rs, ok := reg.Get(log.RuleVersion)
	if !ok {
		return nil, rules.ErrUnknownVersion
	}
	e := engine.New(log.MatchID, rs, cfg)
	for _, entry := range log.Entries {
		if entry.RejectedReason != "" {
			e.RejectReplay(entry.Op, entry.ReceivedAt, entry.RejectedReason)
			continue
		}
		e.ApplyReplay(entry.Op, entry.ReceivedAt)
	}
	_, verdicts := e.Snapshot()
	return verdicts, nil
}

// Equal compares two verdict lists for identical conclusions.
func Equal(a, b []*model.Verdict) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].VerdictID != b[i].VerdictID ||
			a[i].Kind != b[i].Kind ||
			a[i].RuleVersion != b[i].RuleVersion ||
			a[i].StateVersion != b[i].StateVersion ||
			a[i].PlayerID != b[i].PlayerID ||
			a[i].Seq != b[i].Seq ||
			len(a[i].Reasons) != len(b[i].Reasons) ||
			len(a[i].Findings) != len(b[i].Findings) {
			return false
		}
		for j := range a[i].Reasons {
			if a[i].Reasons[j] != b[i].Reasons[j] {
				return false
			}
		}
		for j := range a[i].Findings {
			f1, f2 := a[i].Findings[j], b[i].Findings[j]
			if f1.RuleID != f2.RuleID || f1.Severity != f2.Severity ||
				f1.Observed != f2.Observed || f1.Allowed != f2.Allowed {
				return false
			}
		}
	}
	return true
}
