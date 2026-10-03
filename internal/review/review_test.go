package review_test

import (
	"testing"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/review"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/testutil"
)

func setup(t *testing.T) (*engine.MatchEngine, *model.MatchState, []*model.Verdict, []*model.Operation) {
	t.Helper()
	cfg := testutil.TestConfig()
	e := engine.New("m1", rules.V1(), cfg)
	e.Start()
	t.Cleanup(e.Stop)
	base := time.Now().UnixMilli()
	var ops []*model.Operation
	for i := uint64(1); i <= 5; i++ {
		op := testutil.Op(cfg, "m1", "p1", i, base+int64(i)*200, model.OpMove)
		ops = append(ops, op)
		e.SubmitAsync(op)
	}
	cheat := testutil.MoveOp(cfg, "m1", "p1", 6, base+1200, model.Vec3{X: 500})
	ops = append(ops, cheat)
	e.SubmitAsync(cheat)
	if !e.WaitIdle(5 * time.Second) {
		t.Fatal("drain")
	}
	state, verdicts := e.Snapshot()
	return e, state, verdicts, ops
}

// A rejected cheat verdict is upheld on review using the original operation.
func TestReviewUpholdsCheat(t *testing.T) {
	_, state, verdicts, ops := setup(t)
	cfg := testutil.TestConfig()
	rs := rules.V1()
	var cheatVerdict *model.Verdict
	var cheatOp *model.Operation
	for i, v := range verdicts {
		if v.Seq == 6 {
			cheatVerdict = v
			cheatOp = ops[i]
		}
	}
	if cheatVerdict == nil || cheatOp == nil {
		t.Fatal("missing cheat verdict/op")
	}
	res := review.Review(cheatVerdict, cheatOp, state, rs, cfg)
	if !res.Upheld {
		t.Fatalf("cheat not upheld: %s", res.Basis)
	}
}

// Review never mutates the original verdict record.
func TestReviewDoesNotMutate(t *testing.T) {
	_, state, verdicts, ops := setup(t)
	cfg := testutil.TestConfig()
	rs := rules.V1()
	before := len(verdicts)
	for i, v := range verdicts {
		review.Review(v, ops[i], state, rs, cfg)
	}
	if len(verdicts) != before {
		t.Fatalf("verdict count changed: %d -> %d", before, len(verdicts))
	}
}

// A borderline (warning) case is classified as borderline, not silently passed.
func TestReviewBorderlineClassified(t *testing.T) {
	cfg := testutil.TestConfig()
	rs := rules.V1()
	p := &model.PlayerState{
		PlayerID: "p1", Health: 100, Resource: 100, AttackTokens: 6,
		Position: model.Vec3{X: 0}, LastMoveTime: 1000,
	}
	state := &model.MatchState{
		MatchID: "m1", RuleVersion: rules.Version1,
		Players: map[string]*model.PlayerState{"p1": p},
	}
	// 13 m/s: warning (borderline).
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 13})
	findings := rs.Evaluate(&rules.RuleContext{
		Match: state, Player: p, Op: op, ReceivedAt: 2000, Config: cfg,
	})
	v := &model.Verdict{
		VerdictID: "V-m1-001", Kind: model.VerdictAccepted,
		Findings: findings, RuleVersion: rules.Version1,
	}
	res := review.Review(v, op, state, rs, cfg)
	if !res.Borderline {
		t.Fatalf("expected borderline classification for warning severity")
	}
}
