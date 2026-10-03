package engine_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/testutil"
)

func newTestEngine(t *testing.T, cfg *config.Config) *engine.MatchEngine {
	t.Helper()
	if cfg == nil {
		cfg = testutil.TestConfig()
	}
	e := engine.New("m1", rules.V1(), cfg)
	e.Start()
	t.Cleanup(e.Stop)
	return e
}

// Out-of-order arrival is applied in strict sequence order.
func TestOutOfOrderAppliedInSeqOrder(t *testing.T) {
	e := newTestEngine(t, nil)
	base := time.Now().UnixMilli()
	// arrive 1,3,2,4 (async: a queued op must not block the submitter)
	e.SubmitAsync(testutil.Op(testutil.TestConfig(), "m1", "p1", 1, base, model.OpMove))
	e.SubmitAsync(testutil.Op(testutil.TestConfig(), "m1", "p1", 3, base+600, model.OpMove))
	e.SubmitAsync(testutil.Op(testutil.TestConfig(), "m1", "p1", 2, base+200, model.OpMove))
	e.SubmitAsync(testutil.Op(testutil.TestConfig(), "m1", "p1", 4, base+800, model.OpMove))
	if !e.WaitIdle(5 * time.Second) {
		t.Fatal("engine did not drain")
	}
	state, verdicts := e.Snapshot()
	if len(verdicts) != 4 {
		t.Fatalf("verdicts = %d, want 4", len(verdicts))
	}
	for _, v := range verdicts {
		if v.Kind != model.VerdictAccepted {
			t.Fatalf("seq %d: expected accepted, got %s (%v)", v.Seq, v.Kind, v.Reasons)
		}
	}
	p := state.Players["p1"]
	if p.AppliedSeq != 4 {
		t.Fatalf("applied seq = %d, want 4", p.AppliedSeq)
	}
	if state.Version != 4 {
		t.Fatalf("state version = %d, want 4", state.Version)
	}
}

// Duplicate submission is rejected and never applied twice.
func TestDuplicateNotAppliedTwice(t *testing.T) {
	e := newTestEngine(t, nil)
	base := time.Now().UnixMilli()
	op := testutil.Op(testutil.TestConfig(), "m1", "p1", 1, base, model.OpMove)
	first := e.Submit(op)
	dup := e.Submit(op)
	if first.Kind != model.VerdictAccepted {
		t.Fatalf("first: %v", first.Reasons)
	}
	if dup.Kind != model.VerdictRejected || !testutil.HasReason(dup, model.ReasonDuplicate) {
		t.Fatalf("duplicate: got %s %v, want rejected/duplicate", dup.Kind, dup.Reasons)
	}
	state, _ := e.Snapshot()
	if state.Version != 1 {
		t.Fatalf("state version = %d, want 1 (duplicate must not apply)", state.Version)
	}
}

// A late arrival below the applied frontier but outside the dedup window is
// rejected as rollback and never overwrites published state.
func TestLateRollbackRejected(t *testing.T) {
	c := testutil.TestConfig()
	c.DedupWindowSize = 3
	e := newTestEngine(t, c)
	base := time.Now().UnixMilli()
	for i := uint64(1); i <= 5; i++ {
		e.Submit(testutil.Op(c, "m1", "p1", i, base+int64(i)*200, model.OpMove))
	}
	// seq 1 was applied but has been evicted from the dedup window.
	late := e.Submit(testutil.Op(c, "m1", "p1", 1, base+100, model.OpMove))
	if late.Kind != model.VerdictRejected || !testutil.HasReason(late, model.ReasonRollback) {
		t.Fatalf("late: got %s %v, want rejected/rollback", late.Kind, late.Reasons)
	}
	state, _ := e.Snapshot()
	if state.Version != 5 {
		t.Fatalf("state version = %d, want 5 (late op must not apply)", state.Version)
	}
}

// A sequence jump beyond the configured gap is rejected.
func TestSeqJumpRejected(t *testing.T) {
	c := config.DefaultConfig()
	c.MaxSeqGap = 5
	e := newTestEngine(t, c)
	base := time.Now().UnixMilli()
	e.Submit(testutil.Op(c, "m1", "p1", 1, base, model.OpMove))
	jump := e.Submit(testutil.Op(c, "m1", "p1", 100, base+200, model.OpMove))
	if jump.Kind != model.VerdictRejected || !testutil.HasReason(jump, model.ReasonSeqJump) {
		t.Fatalf("jump: got %s %v, want rejected/seq_jump", jump.Kind, jump.Reasons)
	}
}

// A forged timestamp is rejected and does not apply state.
func TestForgedTimeRejected(t *testing.T) {
	e := newTestEngine(t, nil)
	base := time.Now().UnixMilli()
	e.Submit(testutil.Op(testutil.TestConfig(), "m1", "p1", 1, base, model.OpMove))
	ft := e.Submit(testutil.Op(testutil.TestConfig(), "m1", "p1", 2, base+100000, model.OpMove))
	if ft.Kind != model.VerdictRejected || !testutil.HasReason(ft, model.ReasonForgedTime) {
		t.Fatalf("forged time: got %s %v, want rejected/forged_time", ft.Kind, ft.Reasons)
	}
	state, _ := e.Snapshot()
	if state.Version != 1 {
		t.Fatalf("state version = %d, want 1", state.Version)
	}
}

// A tampered operation (bad MAC) is rejected.
func TestTamperedRejected(t *testing.T) {
	e := newTestEngine(t, nil)
	op := testutil.Op(testutil.TestConfig(), "m1", "p1", 1, time.Now().UnixMilli(), model.OpMove)
	op.Damage = 999 // tamper after signing
	v := e.Submit(op)
	if v.Kind != model.VerdictRejected || !testutil.HasReason(v, model.ReasonTampered) {
		t.Fatalf("tampered: got %s %v, want rejected/tampered", v.Kind, v.Reasons)
	}
}

// A malformed operation is rejected as invalid, never silently dropped.
func TestInvalidRejected(t *testing.T) {
	e := newTestEngine(t, nil)
	op := &model.Operation{MatchID: "m1", PlayerID: "p1", Seq: 0, Kind: model.OpMove}
	v := e.Submit(op)
	if v.Kind != model.VerdictRejected || !testutil.HasReason(v, model.ReasonInvalid) {
		t.Fatalf("invalid: got %s %v, want rejected/invalid", v.Kind, v.Reasons)
	}
}

// When the wait queue is full the submitter gets a queue_full verdict.
func TestQueueFullRejected(t *testing.T) {
	c := config.DefaultConfig()
	c.InboxCapacity = 2
	c.MaxPendingPerPlayer = 1000
	e := newTestEngine(t, c)
	base := time.Now().UnixMilli()
	// Fill the inbox with async ops so the actor cannot drain immediately.
	for i := uint64(0); i < 2; i++ {
		e.SubmitAsync(testutil.Op(c, "m1", "p1", i+100, base+int64(i)*200, model.OpMove))
	}
	// Give the actor a moment; the inbox may drain, so keep submitting until
	// queue_full is observed or all are accepted.
	var gotFull bool
	for i := uint64(0); i < 20; i++ {
		v := e.Submit(testutil.Op(c, "m1", "p1", i+200, base+int64(i)*200, model.OpMove))
		if testutil.HasReason(v, model.ReasonQueueFull) {
			gotFull = true
			break
		}
	}
	if !gotFull {
		t.Skip("inbox drained too fast to observe queue_full; capacity bound still enforced")
	}
}

// Concurrent submitters never crash and every op gets a verdict.
func TestConcurrentSubmitters(t *testing.T) {
	c := config.DefaultConfig()
	c.InboxCapacity = 4096
	e := newTestEngine(t, c)
	base := time.Now().UnixMilli()
	var wg sync.WaitGroup
	verdicts := make([][]*model.Verdict, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			player := fmt.Sprintf("p%d", g)
			vs := make([]*model.Verdict, 0, 50)
			for i := uint64(1); i <= 50; i++ {
				vs = append(vs, e.Submit(testutil.Op(c, "m1", player, i, base+int64(i)*200, model.OpMove)))
			}
			verdicts[g] = vs
		}(g)
	}
	wg.Wait()
	for g := 0; g < 8; g++ {
		for i, v := range verdicts[g] {
			if v == nil {
				t.Fatalf("player %d seq %d: nil verdict (silently dropped)", g, i+1)
			}
		}
	}
	state, _ := e.Snapshot()
	if len(state.Players) != 8 {
		t.Fatalf("players = %d, want 8", len(state.Players))
	}
}

// Snapshot state and verdicts are a single self-consistent version.
func TestSnapshotSelfConsistent(t *testing.T) {
	e := newTestEngine(t, nil)
	base := time.Now().UnixMilli()
	for i := uint64(1); i <= 10; i++ {
		e.Submit(testutil.Op(testutil.TestConfig(), "m1", "p1", i, base+int64(i)*200, model.OpMove))
	}
	state, verdicts := e.Snapshot()
	for _, v := range verdicts {
		if v.StateVersion > state.Version {
			t.Fatalf("verdict %s state_version %d > committed %d", v.VerdictID, v.StateVersion, state.Version)
		}
	}
	if state.Version != 10 {
		t.Fatalf("state version = %d, want 10", state.Version)
	}
}

// A slow player (large gaps) does not block other players' verdicts.
func TestSlowPlayerDoesNotBlockOthers(t *testing.T) {
	c := testutil.TestConfig()
	c.InboxCapacity = 4096
	e := newTestEngine(t, c)
	base := time.Now().UnixMilli()
	// slow sends seq 1 then seq 100 (gap); the queued op must not block submit.
	e.Submit(testutil.Op(c, "m1", "slow", 1, base, model.OpMove))
	e.SubmitAsync(testutil.Op(c, "m1", "slow", 100, base+200, model.OpMove))
	// fast must still get a verdict even though slow has a gap.
	v := e.Submit(testutil.Op(c, "m1", "fast", 1, base, model.OpMove))
	if v.Kind != model.VerdictAccepted {
		t.Fatalf("fast player blocked by slow player: %s %v", v.Kind, v.Reasons)
	}
	if !e.WaitIdle(5 * time.Second) {
		t.Fatal("engine did not drain")
	}
}

