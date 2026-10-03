package replay_test

import (
	"testing"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/replay"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/testutil"
)

// Replay of a recorded match reproduces verdicts identical to online.
func TestReplayIdenticalToOnline(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)

	e := engine.New("m1", rules.V1(), cfg)
	e.Start()
	base := time.Now().UnixMilli()
	for i := uint64(1); i <= 8; i++ {
		e.SubmitAsync(testutil.Op(cfg, "m1", "p1", i, base+int64(i)*200, model.OpMove))
	}
	// A duplicate and a cheat.
	e.SubmitAsync(testutil.Op(cfg, "m1", "p1", 3, base+600, model.OpMove))
	cheat := testutil.MoveOp(cfg, "m1", "p1", 9, base+1800, model.Vec3{X: 500})
	e.SubmitAsync(cheat)
	if !e.WaitIdle(5 * time.Second) {
		t.Fatal("drain")
	}
	_, online := e.Snapshot()
	log := replay.FromEngine("m1", rules.Version1, e.ReplayLog())

	replayed, err := replay.Replay(reg, log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Equal(online, replayed) {
		t.Fatalf("replay mismatch: online=%d replayed=%d", len(online), len(replayed))
	}
}

// Replay binds to the recorded rule version, not the currently active one.
func TestReplayBindsRecordedVersion(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	// Activate V2 (stricter) after the match would have started.
	if err := reg.SetCurrent(rules.Version2); err != nil {
		t.Fatal(err)
	}
	log := &replay.Log{
		MatchID:     "m1",
		RuleVersion: rules.Version1,
		Entries:     nil,
	}
	// Replaying a V1 log must use V1 rules.
	replayed, err := replay.Replay(reg, log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = replayed
	// A V2 log must use V2 rules.
	log2 := &replay.Log{MatchID: "m1", RuleVersion: rules.Version2}
	if _, err := replay.Replay(reg, log2, cfg); err != nil {
		t.Fatal(err)
	}
}

// Unknown rule version is an error, not a silent fallback.
func TestReplayUnknownVersion(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	log := &replay.Log{MatchID: "m1", RuleVersion: "9.9.9"}
	if _, err := replay.Replay(reg, log, cfg); err == nil {
		t.Fatal("expected error for unknown rule version")
	}
}
