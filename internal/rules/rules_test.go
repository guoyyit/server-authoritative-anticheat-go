package rules_test

import (
	"testing"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/testutil"
)

func ctx(cfg *config.Config, p *model.PlayerState, op *model.Operation, recv int64) *rules.RuleContext {
	return &rules.RuleContext{
		Match:      &model.MatchState{MatchID: "m1", Players: map[string]*model.PlayerState{"p1": p}},
		Player:     p,
		Op:         op,
		ReceivedAt: recv,
		Config:     cfg,
	}
}

func player() *model.PlayerState {
	return &model.PlayerState{
		PlayerID:     "p1",
		Health:       100,
		Resource:     100,
		AttackTokens: 6,
		AppliedSeqs:  map[uint64]struct{}{},
	}
}

// Speed inside the tolerance band produces no finding (normal jitter).
func TestSpeedWithinToleranceNoFinding(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	p.LastMoveTime = 1000
	// 11 m/s is within the 20% tolerance band (12 m/s).
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 11})
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	for _, f := range findings {
		if f.RuleID == "speed" {
			t.Fatalf("speed finding inside tolerance band: %+v", f)
		}
	}
}

// A single slight exceedance is a warning, not a rejection.
func TestSpeedSingleSpikeWarning(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	p.LastMoveTime = 1000
	// 13 m/s: above tolerance (12) but below warn ratio (13).
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 13})
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	var speed *model.Finding
	for i := range findings {
		if findings[i].RuleID == "speed" {
			speed = &findings[i]
		}
	}
	if speed == nil {
		t.Fatal("expected a speed finding for a 13 m/s spike")
	}
	if speed.Severity != model.SeverityWarning {
		t.Fatalf("single spike severity = %s, want warning", speed.Severity)
	}
}

// Sustained violations escalate to minor (rejectable).
func TestSpeedSustainedEscalates(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	p.LastMoveTime = 1000
	p.SpeedViolations = 2
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 13})
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	for _, f := range findings {
		if f.RuleID == "speed" && model.SeverityRank(f.Severity) < model.SeverityRank(model.SeverityMinor) {
			t.Fatalf("sustained severity = %s, want >= minor", f.Severity)
		}
	}
}

// A blatant speed hack is critical.
func TestSpeedHackCritical(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	p.LastMoveTime = 1000
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 100})
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	found := false
	for _, f := range findings {
		if f.RuleID == "speed" && f.Severity == model.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected critical speed finding, got %+v", findings)
	}
}

// Teleport is critical regardless of jitter.
func TestTeleportCritical(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	p.LastMoveTime = 1000
	op := testutil.MoveOp(cfg, "m1", "p1", 2, 2000, model.Vec3{X: 150})
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	found := false
	for _, f := range findings {
		if f.RuleID == "teleport" && f.Severity == model.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected critical teleport finding, got %+v", findings)
	}
}

// Attack burst is allowed; sustained over-rate is flagged.
func TestAttackRateBurstAndSustained(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.AttackTokens = 6
	p.LastAttackTime = 1000
	// Attacks at 50ms intervals = 20/s; burst 6 absorbs the first, then the
	// token bucket depletes and over-rate fire is flagged.
	var flagged bool
	for i := 1; i <= 12; i++ {
		t := 1000 + int64(i)*50
		op := testutil.AttackOp(cfg, "m1", "p1", uint64(i), t, "p2", 10)
		findings := rules.V1().Evaluate(ctx(cfg, p, op, t))
		for _, f := range findings {
			if f.RuleID == "attack_rate" {
				flagged = true
			}
		}
		// Simulate applyState token consumption (rule is pure).
		dt := 0.0
		if p.LastAttackTime != 0 {
			dt = float64(t-p.LastAttackTime) / 1000.0
		}
		p.AttackTokens = min(float64(cfg.AttackBurst), p.AttackTokens+dt*cfg.MaxAttackRate) - 1
		p.LastAttackTime = t
	}
	if !flagged {
		t.Fatal("sustained over-rate fire was not flagged")
	}
}

// Insufficient resource is major.
func TestInsufficientResource(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Resource = 0
	p.LastRegenTime = 0 // no regen
	op := testutil.AttackOp(cfg, "m1", "p1", 1, 2000, "p2", 10)
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	found := false
	for _, f := range findings {
		if f.RuleID == "resource" && f.Severity == model.SeverityMajor {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected major resource finding, got %+v", findings)
	}
}

// Damage over the per-hit cap is major.
func TestDamageOverCap(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	op := testutil.AttackOp(cfg, "m1", "p1", 1, 2000, "p2", 999)
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	found := false
	for _, f := range findings {
		if f.RuleID == "damage" && f.Severity == model.SeverityMajor {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected major damage finding, got %+v", findings)
	}
}

// Forged time: non-monotonic client clock.
func TestForgedTimeNonMonotonic(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.LastOpTime = 2000
	p.LastServerTime = 2000
	op := testutil.Op(cfg, "m1", "p1", 2, 1000, model.OpMove) // client went backwards
	findings := rules.V1().Evaluate(ctx(cfg, p, op, 2000))
	found := false
	for _, f := range findings {
		if f.RuleID == "time_skew" && f.Severity == model.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected critical time_skew finding, got %+v", findings)
	}
}

// Realistic network jitter (small client-time variations) must not false-positive.
func TestNetworkJitterNoFalsePositive(t *testing.T) {
	cfg := testutil.TestConfig()
	p := player()
	p.Position = model.Vec3{X: 0}
	// Simulate 20 moves with realistic jitter: client dt ~200ms with +-30ms
	// variation, positions ~1m apart (5 m/s). Server receives in bursts.
	clientT := int64(1000)
	serverT := int64(1000)
	for i := 1; i <= 20; i++ {
		dt := int64(200 + (i%3)*15 - 15) // 185..215ms jitter
		clientT += dt
		serverT += dt - int64(i%2)*40 // server receives with bursty jitter
		op := testutil.MoveOp(cfg, "m1", "p1", uint64(i), clientT, model.Vec3{X: float64(i)})
		findings := rules.V1().Evaluate(ctx(cfg, p, op, serverT))
		for _, f := range findings {
			if f.RuleID == "speed" || f.RuleID == "teleport" || f.RuleID == "time_skew" {
				t.Fatalf("jitter false positive at step %d: %+v", i, f)
			}
		}
		p.Position = op.Position
		p.LastMoveTime = op.ClientTime
		p.LastOpTime = op.ClientTime
		p.LastServerTime = serverT
	}
}
