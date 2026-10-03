// Command demo runs an in-process demonstration of the authoritative
// anti-cheat pipeline: loopback transport, per-match sequencing, rule
// validation, deterministic replay and appeal review.
package main

import (
	"fmt"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/replay"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/review"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/security"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/server"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/transport"
)

func makeOp(cfg *config.Config, match, player string, seq uint64, t int64, kind model.OpKind) *model.Operation {
	op := &model.Operation{
		MatchID: match, PlayerID: player, Seq: seq, ClientTime: t, Kind: kind,
		Position: model.Vec3{X: float64(seq), Y: 0, Z: 0},
		Nonce:    seq,
	}
	if kind == model.OpAttack {
		op.TargetID = "p2"
		op.Damage = 10
	}
	op.MAC = security.Sign(op, cfg.MACKey)
	return op
}

func printVerdicts(verdicts []*model.Verdict) {
	for _, v := range verdicts {
		fmt.Printf("  %s seq=%d kind=%s reasons=%v", v.VerdictID, v.Seq, v.Kind, v.Reasons)
		for _, f := range v.Findings {
			fmt.Printf("\n    [%s] %s observed=%s allowed=%s basis=%s", f.Severity, f.Title, f.Observed, f.Allowed, f.Basis)
		}
		fmt.Println()
	}
}

func main() {
	cfg := config.DefaultConfig()
	registry := rules.NewRegistry()
	rules.RegisterBuiltins(registry)

	srv := server.New(cfg, registry)
	defer srv.Stop()

	matchID := "m1"
	engine, err := srv.StartMatch(matchID)
	if err != nil {
		panic(err)
	}

	// --- loopback transport: client ops arrive over TCP loopback ---
	loopSrv, err := transport.NewLoopbackServer()
	if err != nil {
		panic(err)
	}
	defer loopSrv.Close()
	go func() {
		_ = loopSrv.Serve(func(op *model.Operation) {
			_, _ = srv.Submit(op)
		})
	}()
	client, err := transport.DialLoopback(loopSrv.Addr())
	if err != nil {
		panic(err)
	}
	defer client.Close()

	base := time.Now().UnixMilli()
	send := func(op *model.Operation) {
		if err := client.Send(op); err != nil {
			panic(err)
		}
	}

	// normal moves for p1 (10 m/s cap): 1m per 200ms = 5 m/s, clean
	for i := uint64(1); i <= 3; i++ {
		op := makeOp(cfg, matchID, "p1", i, base+int64(i)*200, model.OpMove)
		op.Position = model.Vec3{X: float64(i), Y: 0, Z: 0}
		op.MAC = security.Sign(op, cfg.MACKey)
		send(op)
	}
	// speed hack: 200m in 200ms
	cheat := makeOp(cfg, matchID, "p1", 4, base+800, model.OpMove)
	cheat.Position = model.Vec3{X: 200, Y: 0, Z: 0}
	cheat.MAC = security.Sign(cheat, cfg.MACKey)
	send(cheat)
	// duplicate of seq 3
	dup := makeOp(cfg, matchID, "p1", 3, base+600, model.OpMove)
	dup.Position = model.Vec3{X: 3, Y: 0, Z: 0}
	dup.MAC = security.Sign(dup, cfg.MACKey)
	send(dup)
	// late rollback: seq 2 after the frontier has advanced past it
	late := makeOp(cfg, matchID, "p1", 2, base+300, model.OpMove)
	late.Position = model.Vec3{X: 2, Y: 0, Z: 0}
	late.MAC = security.Sign(late, cfg.MACKey)
	send(late)
	// forged time: way in the future
	ft := makeOp(cfg, matchID, "p1", 5, base+100000, model.OpMove)
	ft.Position = model.Vec3{X: 201, Y: 0, Z: 0}
	ft.MAC = security.Sign(ft, cfg.MACKey)
	send(ft)
	// tampered: valid MAC then altered
	tamp := makeOp(cfg, matchID, "p1", 6, base+1000, model.OpMove)
	tamp.Position = model.Vec3{X: 202, Y: 0, Z: 0}
	tamp.MAC = security.Sign(tamp, cfg.MACKey)
	tamp.Damage = 999
	send(tamp)
	// attack spam for p2 (4/s cap, burst 6)
	for i := uint64(1); i <= 10; i++ {
		op := makeOp(cfg, matchID, "p2", i, base+int64(i)*50, model.OpAttack)
		op.MAC = security.Sign(op, cfg.MACKey)
		send(op)
	}

	if !engine.WaitIdle(5 * time.Second) {
		fmt.Println("warning: engine did not drain")
	}

	state, verdicts := engine.Snapshot()
	fmt.Printf("match %s rule=%s state_version=%d verdicts=%d\n", matchID, state.RuleVersion, state.Version, len(verdicts))
	printVerdicts(verdicts)

	// --- deterministic replay ---
	log := replay.FromEngine(matchID, state.RuleVersion, engine.ReplayLog())
	replayed, err := replay.Replay(registry, log, cfg)
	if err != nil {
		panic(err)
	}
	fmt.Printf("\nreplay verdicts: %d (identical to online: %v)\n", len(replayed), replay.Equal(verdicts, replayed))

	// --- appeal review of the speed-hack verdict ---
	var appeal *model.Verdict
	for _, v := range verdicts {
		if v.PlayerID == "p1" && v.Seq == 4 {
			appeal = v
		}
	}
	if appeal != nil {
		rs, _ := registry.Get(state.RuleVersion)
		res := review.Review(appeal, cheat, state, rs, cfg)
		fmt.Printf("\nreview of %s: upheld=%v borderline=%v basis=%s\n", res.VerdictID, res.Upheld, res.Borderline, res.Basis)
	}

	// --- rule version switching: new match binds to current version ---
	if err := registry.SetCurrent(rules.Version2); err != nil {
		panic(err)
	}
	m2, err := srv.StartMatch("m2")
	if err != nil {
		panic(err)
	}
	s2, _ := m2.Snapshot()
	fmt.Printf("\nm2 bound to rule version %s; m1 still at %s (historical verdicts not rewritten)\n", s2.RuleVersion, state.RuleVersion)
}
