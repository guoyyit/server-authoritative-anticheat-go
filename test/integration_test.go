package integration_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/replay"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/server"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/testutil"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/transport"
)

// Normal network jitter through a jitter pipe must not cause false positives.
func TestNetworkJitterNoFalsePositive(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	srv := server.New(cfg, reg)
	defer srv.Stop()

	engine, err := srv.StartMatch("m1")
	if err != nil {
		t.Fatal(err)
	}

	pipe := transport.NewMemoryPipe(64)
	jitter := transport.NewJitterPipe(pipe, 20*time.Millisecond, 15*time.Millisecond, 7)
	defer jitter.Close()

	base := time.Now().UnixMilli()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 15; i++ {
			op := testutil.MoveOp(cfg, "m1", "p1", i, base+int64(i)*200, model.Vec3{X: float64(i)})
			if err := jitter.Send(op); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 15; i++ {
			op, err := jitter.Recv()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := srv.Submit(op); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()

	if !engine.WaitIdle(5 * time.Second) {
		t.Fatal("drain")
	}
	_, verdicts := engine.Snapshot()
	for _, v := range verdicts {
		if v.Kind == model.VerdictRejected {
			t.Fatalf("jitter caused rejection: seq=%d reasons=%v findings=%v", v.Seq, v.Reasons, v.Findings)
		}
	}
}

// Rule version switching: a match keeps its bound version; a new match uses
// the current version; historical verdicts are not rewritten.
func TestRuleVersionSwitching(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	srv := server.New(cfg, reg)
	defer srv.Stop()

	m1, err := srv.StartMatch("m1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixMilli()
	m1.Submit(testutil.Op(cfg, "m1", "p1", 1, base, model.OpMove))
	if !m1.WaitIdle(2 * time.Second) {
		t.Fatal("drain m1")
	}
	s1, _ := m1.Snapshot()
	if s1.RuleVersion != rules.Version1 {
		t.Fatalf("m1 version = %s, want %s", s1.RuleVersion, rules.Version1)
	}

	// Upgrade the active rule version.
	if err := reg.SetCurrent(rules.Version2); err != nil {
		t.Fatal(err)
	}
	m2, err := srv.StartMatch("m2")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := m2.Snapshot()
	if s2.RuleVersion != rules.Version2 {
		t.Fatalf("m2 version = %s, want %s", s2.RuleVersion, rules.Version2)
	}

	// m1's historical verdicts must still reference V1.
	_, v1 := m1.Snapshot()
	for _, v := range v1 {
		if v.RuleVersion != rules.Version1 {
			t.Fatalf("m1 verdict %s rewritten to %s", v.VerdictID, v.RuleVersion)
		}
	}
}

// Multiple concurrent matches/players: a slow or malicious player does not
// block others, and every input gets a verdict.
func TestConcurrentMatchesAndPlayers(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.InboxCapacity = 4096
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	srv := server.New(cfg, reg)
	defer srv.Stop()

	const matches = 4
	const players = 4
	const ops = 30
	var engines []*engineHandle
	for m := 0; m < matches; m++ {
		id := fmt.Sprintf("m%d", m)
		e, err := srv.StartMatch(id)
		if err != nil {
			t.Fatal(err)
		}
		engines = append(engines, &engineHandle{id: id, e: e})
	}

	var wg sync.WaitGroup
	for _, eh := range engines {
		for p := 0; p < players; p++ {
			wg.Add(1)
			go func(eh *engineHandle, p int) {
				defer wg.Done()
				player := fmt.Sprintf("p%d", p)
				base := time.Now().UnixMilli()
				for i := uint64(1); i <= ops; i++ {
					op := testutil.Op(cfg, eh.id, player, i, base+int64(i)*200, model.OpMove)
					v := eh.e.Submit(op)
					if v == nil {
						t.Errorf("nil verdict for %s/%s seq %d", eh.id, player, i)
						return
					}
				}
			}(eh, p)
		}
	}
	wg.Wait()

	for _, eh := range engines {
		if !eh.e.WaitIdle(5 * time.Second) {
			t.Fatalf("match %s did not drain", eh.id)
		}
		state, verdicts := eh.e.Snapshot()
		if len(state.Players) != players {
			t.Fatalf("match %s players = %d, want %d", eh.id, len(state.Players), players)
		}
		if len(verdicts) != matches*0 + players*ops {
			t.Fatalf("match %s verdicts = %d, want %d", eh.id, len(verdicts), players*ops)
		}
	}
}

// End-to-end forged inputs are rejected with distinguishable reasons.
func TestForgedInputsEndToEnd(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	srv := server.New(cfg, reg)
	defer srv.Stop()

	e, err := srv.StartMatch("m1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixMilli()
	cases := []struct {
		name   string
		op     *model.Operation
		reason model.ReasonCode
	}{
		{"tampered", tampered(cfg, base), model.ReasonTampered},
		{"forged_time", testutil.Op(cfg, "m1", "p1", 1, base+100000, model.OpMove), model.ReasonForgedTime},
		{"invalid", &model.Operation{MatchID: "m1", PlayerID: "p1", Seq: 0}, model.ReasonInvalid},
	}
	for _, tc := range cases {
		v, err := srv.Submit(tc.op)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if v.Kind != model.VerdictRejected || !testutil.HasReason(v, tc.reason) {
			t.Fatalf("%s: got %s %v, want rejected/%s", tc.name, v.Kind, v.Reasons, tc.reason)
		}
	}
	_ = e
}

// Replay of a match with forged inputs reproduces identical verdicts.
func TestReplayWithForgedInputs(t *testing.T) {
	cfg := testutil.TestConfig()
	reg := rules.NewRegistry()
	rules.RegisterBuiltins(reg)
	srv := server.New(cfg, reg)
	defer srv.Stop()

	e, err := srv.StartMatch("m1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixMilli()
	e.SubmitAsync(testutil.Op(cfg, "m1", "p1", 1, base, model.OpMove))
	e.SubmitAsync(tampered(cfg, base))
	e.SubmitAsync(testutil.Op(cfg, "m1", "p1", 2, base+400, model.OpMove))
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

type engineHandle struct {
	id string
	e  *engine.MatchEngine
}

func tampered(cfg *config.Config, base int64) *model.Operation {
	op := testutil.Op(cfg, "m1", "p1", 99, base, model.OpMove)
	op.Damage = 999
	return op
}
