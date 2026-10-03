// Package engine implements the per-match, single-writer validation engine.
// Client operations arrive over a bounded in-memory wait queue; the engine
// applies them in strict sequence order, never applying duplicates and never
// letting late operations overwrite published state. Every decision is
// recorded as an append-only, bounded verdict log.
package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/security"
)

// RecordedOp is one operation with the server receive timestamp and, for
// operations rejected at the submit boundary, the rejection reason. It is the
// unit of deterministic replay.
type RecordedOp struct {
	Op             *model.Operation `json:"op"`
	ReceivedAt     int64            `json:"received_at"`
	RejectedReason model.ReasonCode `json:"rejected_reason,omitempty"`
}

// envelope is one wait-queue entry.
type envelope struct {
	op         *model.Operation
	receivedAt int64
	reply      chan *model.Verdict
}

// MatchEngine validates operations for one match. It is safe for concurrent
// submitters; all state mutation happens on the engine's own goroutine while
// holding the state mutex.
type MatchEngine struct {
	cfg   *config.Config
	rules *rules.RuleSet

	mu       sync.Mutex // protects state, verdicts, replay log
	state    *model.MatchState
	verdicts []*model.Verdict
	log      []RecordedOp

	// per-player pending out-of-order envelopes (actor only)
	pending map[string]map[uint64]*envelope

	inbox chan *envelope
	stop  chan struct{}
	done  chan struct{}

	verdictSeq uint64
}

// New creates an engine for a match bound to the given rule set.
func New(matchID string, rs *rules.RuleSet, cfg *config.Config) *MatchEngine {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	e := &MatchEngine{
		cfg:   cfg,
		rules: rs,
		state: &model.MatchState{
			MatchID:     matchID,
			RuleVersion: rs.Version,
			Players:     make(map[string]*model.PlayerState),
			StartedAt:   nowMS(),
		},
		pending: make(map[string]map[uint64]*envelope),
		inbox:   make(chan *envelope, cfg.InboxCapacity),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	return e
}

// Start launches the engine goroutine.
func (e *MatchEngine) Start() {
	go e.run()
}

// Stop drains the wait queue and stops the engine goroutine.
func (e *MatchEngine) Stop() {
	close(e.stop)
	<-e.done
}

// Submit enqueues one operation and returns its verdict. It never blocks on
// other matches; when the wait queue is full a queue_full verdict is produced.
func (e *MatchEngine) Submit(op *model.Operation) *model.Verdict {
	return e.submit(op, true)
}

// SubmitAsync enqueues one operation without waiting for its verdict.
func (e *MatchEngine) SubmitAsync(op *model.Operation) {
	e.submit(op, false)
}

// Snapshot returns a self-consistent copy of the committed state together
// with the verdict log.
func (e *MatchEngine) Snapshot() (*model.MatchState, []*model.Verdict) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := cloneState(e.state)
	verdicts := make([]*model.Verdict, len(e.verdicts))
	copy(verdicts, e.verdicts)
	return state, verdicts
}

// ReplayLog returns the recorded (operation, receive-time) stream.
func (e *MatchEngine) ReplayLog() []RecordedOp {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]RecordedOp, len(e.log))
	copy(out, e.log)
	return out
}

// ApplyReplay applies one recorded operation on the calling goroutine. It is
// used by deterministic replay only; online traffic goes through Submit.
func (e *MatchEngine) ApplyReplay(op *model.Operation, receivedAt int64) *model.Verdict {
	return e.process(&envelope{op: op, receivedAt: receivedAt})
}

// RejectReplay reproduces a submit-boundary rejection during replay.
func (e *MatchEngine) RejectReplay(op *model.Operation, receivedAt int64, reason model.ReasonCode) *model.Verdict {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rejectLocked(op, receivedAt, reason, nil)
}

// WaitIdle blocks until the wait queue has drained (or timeout).
func (e *MatchEngine) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(e.inbox) == 0 {
			time.Sleep(2 * time.Millisecond)
			if len(e.inbox) == 0 {
				return true
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return len(e.inbox) == 0
}

func (e *MatchEngine) run() {
	defer close(e.done)
	for {
		select {
		case env := <-e.inbox:
			_ = e.process(env)
		case <-e.stop:
			for {
				select {
				case env := <-e.inbox:
					_ = e.process(env)
				default:
					return
				}
			}
		}
	}
}

func (e *MatchEngine) submit(op *model.Operation, wait bool) *model.Verdict {
	receivedAt := nowMS()

	// Structural and MAC validation happens before enqueue so that malformed
	// input never reaches the wait queue and is never silently dropped.
	e.mu.Lock()
	v := e.structuralCheckLocked(op, receivedAt)
	if v != nil {
		e.recordLogLocked(op, receivedAt, v.Reasons[0])
		e.mu.Unlock()
		return v
	}
	e.mu.Unlock()

	env := &envelope{op: op, receivedAt: receivedAt}
	if wait {
		env.reply = make(chan *model.Verdict, 1)
	}
	select {
	case e.inbox <- env:
	default:
		e.mu.Lock()
		v := e.rejectLocked(op, receivedAt, model.ReasonQueueFull, nil)
		e.recordLogLocked(op, receivedAt, model.ReasonQueueFull)
		e.mu.Unlock()
		return v
	}
	if !wait {
		return nil
	}
	select {
	case v := <-env.reply:
		return v
	case <-time.After(30 * time.Second):
		e.mu.Lock()
		v := e.rejectLocked(op, receivedAt, model.ReasonQueueFull, nil)
		e.mu.Unlock()
		return v
	}
}

// process runs on the engine goroutine. It holds the state mutex for the
// entire validation and application so that concurrent readers always see a
// self-consistent committed version.
func (e *MatchEngine) process(env *envelope) *model.Verdict {
	op := env.op
	receivedAt := env.receivedAt

	e.mu.Lock()
	p := e.playerLocked(op.PlayerID)
	e.recordLogLocked(op, receivedAt, "")

	if v := e.sequenceCheckLocked(p, op, receivedAt); v != nil {
		e.mu.Unlock()
		e.reply(env, v)
		return v
	}

	if e.pending[op.PlayerID] == nil {
		e.pending[op.PlayerID] = make(map[uint64]*envelope)
	}
	e.pending[op.PlayerID][op.Seq] = env

	verdicts := e.drainLocked(p)
	e.mu.Unlock()

	for _, v := range verdicts {
		if v.PlayerID == op.PlayerID && v.Seq == op.Seq {
			return v
		}
	}
	return nil
}

func (e *MatchEngine) reply(env *envelope, v *model.Verdict) {
	if env.reply != nil && v != nil {
		env.reply <- v
	}
}

// sequenceCheckLocked rejects duplicates, rollbacks (late arrivals), seq jumps
// and wait-queue overflow before an operation is enqueued for ordering.
// Caller must hold e.mu.
func (e *MatchEngine) sequenceCheckLocked(p *model.PlayerState, op *model.Operation, receivedAt int64) *model.Verdict {
	if _, dup := p.AppliedSeqs[op.Seq]; dup {
		return e.rejectLocked(op, receivedAt, model.ReasonDuplicate, nil)
	}
	if op.Seq <= p.AppliedSeq {
		// Below the applied frontier: late arrival or forged rollback.
		return e.rejectLocked(op, receivedAt, model.ReasonRollback, nil)
	}
	if pending, ok := e.pending[op.PlayerID][op.Seq]; ok && pending != nil {
		return e.rejectLocked(op, receivedAt, model.ReasonDuplicate, nil)
	}
	gap := op.Seq - p.AppliedSeq - 1
	if gap > e.cfg.MaxSeqGap {
		return e.rejectLocked(op, receivedAt, model.ReasonSeqJump, nil)
	}
	if len(e.pending[op.PlayerID]) >= e.cfg.MaxPendingPerPlayer {
		return e.rejectLocked(op, receivedAt, model.ReasonQueueFull, nil)
	}
	return nil
}

// drainLocked applies the contiguous prefix starting at AppliedSeq+1 and
// replies to each drained envelope. Caller must hold e.mu.
func (e *MatchEngine) drainLocked(p *model.PlayerState) []*model.Verdict {
	var out []*model.Verdict
	for {
		next := p.AppliedSeq + 1
		env, ok := e.pending[p.PlayerID][next]
		if !ok {
			return out
		}
		delete(e.pending[p.PlayerID], next)
		v := e.applyOpLocked(p, env.op, env.receivedAt)
		out = append(out, v)
		e.reply(env, v)
	}
}

// applyOpLocked evaluates rules, decides, applies state and records the
// verdict. Semantic rejections advance the sequence frontier as a skipped hole
// so one bad operation never blocks later operations. Caller must hold e.mu.
func (e *MatchEngine) applyOpLocked(p *model.PlayerState, op *model.Operation, receivedAt int64) *model.Verdict {
	ctx := &rules.RuleContext{
		Match:      e.state,
		Player:     p,
		Op:         op,
		ReceivedAt: receivedAt,
		Config:     e.cfg,
	}

	// Time rule first: forged timestamps are rejected without applying state.
	var timeFindings []model.Finding
	var gameFindings []model.Finding
	for _, rule := range e.rules.Rules {
		findings := rule.Evaluate(ctx)
		if rule.ID() == "time_skew" {
			timeFindings = append(timeFindings, findings...)
		} else {
			gameFindings = append(gameFindings, findings...)
		}
	}
	if len(timeFindings) > 0 {
		v := e.rejectLocked(op, receivedAt, model.ReasonForgedTime, timeFindings)
		e.advanceFrontierLocked(p, op)
		return v
	}

	if model.SeverityRank(model.MaxSeverity(gameFindings)) >= model.SeverityRank(e.cfg.RejectThreshold) {
		v := e.rejectLocked(op, receivedAt, model.ReasonCheat, gameFindings)
		e.advanceFrontierLocked(p, op)
		return v
	}

	e.applyStateLocked(p, op, receivedAt, gameFindings)
	e.advanceFrontierLocked(p, op)
	p.AppliedCount++
	e.state.Version++

	v := e.verdictLocked(op, receivedAt, model.VerdictAccepted, nil, gameFindings)
	e.appendVerdictLocked(v)
	return v
}

// applyStateLocked mutates authoritative state for an accepted operation.
// Caller must hold e.mu.
func (e *MatchEngine) applyStateLocked(p *model.PlayerState, op *model.Operation, receivedAt int64, findings []model.Finding) {
	// Server-authoritative resource regeneration.
	if p.LastRegenTime != 0 {
		dt := float64(op.ClientTime-p.LastRegenTime) / 1000.0
		if dt > 0 {
			p.Resource = min(e.cfg.ResourceMax, p.Resource+dt*e.cfg.ResourceRegen)
		}
	}
	p.LastRegenTime = op.ClientTime

	switch op.Kind {
	case model.OpMove:
		p.Position = op.Position
		p.LastMoveTime = op.ClientTime
	case model.OpAttack:
		p.Resource -= e.cfg.AttackCost
		dt := 0.0
		if p.LastAttackTime != 0 {
			dt = float64(op.ClientTime-p.LastAttackTime) / 1000.0
		}
		p.AttackTokens = max(0, min(float64(e.cfg.AttackBurst), p.AttackTokens+dt*e.cfg.MaxAttackRate)-1)
		p.LastAttackTime = op.ClientTime
		if target := e.state.Players[op.TargetID]; target != nil {
			target.Health = max(0, target.Health-op.Damage)
		}
	}

	p.LastOpTime = op.ClientTime
	p.LastServerTime = receivedAt

	// Violation counters drive escalation; reset on a clean pass.
	hasSpeed, hasAttack := false, false
	for _, f := range findings {
		switch f.RuleID {
		case "speed":
			hasSpeed = true
		case "attack_rate":
			hasAttack = true
		}
	}
	if hasSpeed {
		p.SpeedViolations++
	} else {
		p.SpeedViolations = 0
	}
	if hasAttack {
		p.AttackViolations++
	} else {
		p.AttackViolations = 0
	}
}

// structuralCheckLocked validates identity fields and the MAC before enqueue.
// Caller must hold e.mu.
func (e *MatchEngine) structuralCheckLocked(op *model.Operation, receivedAt int64) *model.Verdict {
	if op == nil || op.MatchID == "" || op.PlayerID == "" || op.Seq == 0 {
		return e.rejectLocked(op, receivedAt, model.ReasonInvalid, nil)
	}
	if op.Kind != model.OpMove && op.Kind != model.OpAttack {
		return e.rejectLocked(op, receivedAt, model.ReasonInvalid, nil)
	}
	if op.Damage < 0 {
		return e.rejectLocked(op, receivedAt, model.ReasonInvalid, nil)
	}
	if !security.Verify(op, e.cfg.MACKey) {
		return e.rejectLocked(op, receivedAt, model.ReasonTampered, nil)
	}
	return nil
}

// playerLocked returns the player state, creating it if needed. Caller must
// hold e.mu.
func (e *MatchEngine) playerLocked(id string) *model.PlayerState {
	p := e.state.Players[id]
	if p == nil {
		p = &model.PlayerState{
			PlayerID:     id,
			Health:       e.cfg.PlayerMaxHealth,
			Resource:     e.cfg.ResourceMax,
			AttackTokens: float64(e.cfg.AttackBurst),
			AppliedSeqs:  make(map[uint64]struct{}),
		}
		e.state.Players[id] = p
	}
	return p
}

// advanceFrontierLocked moves the sequence frontier past an operation.
// Caller must hold e.mu.
func (e *MatchEngine) advanceFrontierLocked(p *model.PlayerState, op *model.Operation) {
	p.AppliedSeq = op.Seq
	p.AppliedSeqs[op.Seq] = struct{}{}
	e.trimDedupLocked(p)
}

// trimDedupLocked evicts the oldest applied seqs until the window is within
// bound. Caller must hold e.mu.
func (e *MatchEngine) trimDedupLocked(p *model.PlayerState) {
	for len(p.AppliedSeqs) > e.cfg.DedupWindowSize {
		var smallest uint64
		first := true
		for seq := range p.AppliedSeqs {
			if first || seq < smallest {
				smallest = seq
				first = false
			}
		}
		delete(p.AppliedSeqs, smallest)
	}
}

// rejectLocked records a rejection verdict. Caller must hold e.mu.
func (e *MatchEngine) rejectLocked(op *model.Operation, receivedAt int64, reason model.ReasonCode, findings []model.Finding) *model.Verdict {
	if op != nil {
		if p := e.state.Players[op.PlayerID]; p != nil {
			p.RejectedCount++
		}
	}
	v := e.verdictLocked(op, receivedAt, model.VerdictRejected, []model.ReasonCode{reason}, findings)
	e.appendVerdictLocked(v)
	return v
}

// verdictLocked builds a verdict. Caller must hold e.mu.
func (e *MatchEngine) verdictLocked(op *model.Operation, receivedAt int64, kind model.VerdictKind, reasons []model.ReasonCode, findings []model.Finding) *model.Verdict {
	e.verdictSeq++
	v := &model.Verdict{
		VerdictID:    fmt.Sprintf("V-%s-%06d", e.state.MatchID, e.verdictSeq),
		MatchID:      e.state.MatchID,
		Kind:         kind,
		Reasons:      reasons,
		Findings:     findings,
		RuleVersion:  e.rules.Version,
		StateVersion: e.state.Version,
		DecidedAt:    receivedAt,
	}
	if op != nil {
		v.PlayerID = op.PlayerID
		v.Seq = op.Seq
	}
	return v
}

// appendVerdictLocked appends a verdict, evicting the oldest when the bound is
// reached. Caller must hold e.mu.
func (e *MatchEngine) appendVerdictLocked(v *model.Verdict) {
	e.verdicts = append(e.verdicts, v)
	if len(e.verdicts) > e.cfg.VerdictStoreCapacity {
		copy(e.verdicts, e.verdicts[1:])
		e.verdicts = e.verdicts[:len(e.verdicts)-1]
	}
}

// recordLogLocked appends to the replay log. Caller must hold e.mu.
func (e *MatchEngine) recordLogLocked(op *model.Operation, receivedAt int64, rejected model.ReasonCode) {
	e.log = append(e.log, RecordedOp{Op: op, ReceivedAt: receivedAt, RejectedReason: rejected})
	if len(e.log) > e.cfg.ReplayLogCapacity {
		copy(e.log, e.log[1:])
		e.log = e.log[:len(e.log)-1]
	}
}

func cloneState(s *model.MatchState) *model.MatchState {
	out := &model.MatchState{
		MatchID:     s.MatchID,
		RuleVersion: s.RuleVersion,
		Version:     s.Version,
		StartedAt:   s.StartedAt,
		Finished:    s.Finished,
		Players:     make(map[string]*model.PlayerState, len(s.Players)),
	}
	for id, p := range s.Players {
		cp := *p
		cp.AppliedSeqs = nil
		cp.AttackTimes = append([]int64(nil), p.AttackTimes...)
		out.Players[id] = &cp
	}
	return out
}

func nowMS() int64 {
	return time.Now().UnixMilli()
}
