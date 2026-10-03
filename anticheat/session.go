package anticheat

import "sync"

// Session 是一局对局的服务端权威校验上下文。
// 开局时绑定规则版本，生命周期内不变；规则升级不影响本对局。
//
// 并发模型：每个玩家一条独立的处理通道（lane），lane 间互不阻塞；
// 单玩家的处理串行化，保证序号机与判定器无需额外同步。
// 状态发布采用不可变快照：任何时刻读到的都是某个已完整提交的版本。
type Session struct {
	matchID string
	rules   *RuleSet
	cfg     Config
	store   *VerdictStore

	lanes map[string]*playerLane // 开局后不可变

	mu       sync.Mutex
	closed   bool
	states   map[string]PlayerState // 权威状态（live）
	version  uint64                 // 已提交快照版本号
	snapshot Snapshot               // 最近一次发布的不可变快照
	verdictN int                    // 已记录判定数（受上限约束）
	journal  []Operation            // 已应用操作的确定性日志，用于重放
	logClock uint64                 // 逻辑时钟：已应用操作计数
}

// playerLane 是单玩家的处理通道。
type playerLane struct {
	playerID string
	key      uint64 // 会话级防篡改密钥

	mu             sync.Mutex
	sem            chan struct{} // 在途操作上界（计数信号量）
	seq            *sequencer
	judge          *judge
	lastClientTime int64
}

func newSession(matchID string, playerIDs []string, rules *RuleSet, cfg Config, store *VerdictStore) *Session {
	s := &Session{
		matchID: matchID,
		rules:   rules,
		cfg:     cfg,
		store:   store,
		lanes:   make(map[string]*playerLane, len(playerIDs)),
		states:  make(map[string]PlayerState, len(playerIDs)),
	}
	for _, pid := range playerIDs {
		s.lanes[pid] = &playerLane{
			playerID: pid,
			key:      deriveKey(matchID, pid),
			sem:      make(chan struct{}, cfg.InboxCapacity),
			seq:      newSequencer(rules.MaxSeqGap, cfg.MaxReorderBuffer),
			judge:    newJudge(rules),
		}
		s.states[pid] = PlayerState{PlayerID: pid, Health: 1000}
	}
	s.publishLocked()
	return s
}

// Submit 提交一条操作，同步返回显式回执。
// 队列满、对局关闭、玩家不存在、校验失败都会以可区分的原因拒绝。
func (s *Session) Submit(op Operation) Receipt {
	lane, ok := s.lanes[op.PlayerID]
	if !ok {
		return Receipt{Accepted: false, Reason: RejectUnknownPlayer,
			Detail: "player " + op.PlayerID + " is not in match " + s.matchID}
	}
	// 在途上界：非阻塞获取，满即拒绝（背压），不拖垮服务。
	select {
	case lane.sem <- struct{}{}:
		defer func() { <-lane.sem }()
	default:
		return Receipt{Accepted: false, Reason: RejectQueueFull,
			Detail: "too many in-flight operations for player " + op.PlayerID}
	}

	lane.mu.Lock()
	defer lane.mu.Unlock()

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Receipt{Accepted: false, Reason: RejectSessionClosed,
			Detail: "match " + s.matchID + " is closed"}
	}

	// 静态校验：防篡改、伪造时间。
	if r := validateInput(op, lane.key, s.rules, lane.lastClientTime); !r.Accepted {
		return r
	}
	if op.ClientTime > lane.lastClientTime {
		lane.lastClientTime = op.ClientTime
	}

	// 排序与去重。
	applied, r := lane.seq.push(op)
	if !r.Accepted {
		return r
	}
	for _, a := range applied {
		s.applyOp(lane, a)
	}
	return Receipt{Accepted: true}
}

// applyOp 应用一条已排序操作：更新权威状态、记录日志、评估规则、发布快照。
// 调用时持有 lane.mu。
func (s *Session) applyOp(lane *playerLane, op Operation) {
	// 会话级临界区：推进逻辑时钟、写日志、更新权威状态。
	s.mu.Lock()
	s.logClock++
	logicalTime := s.logClock
	s.journal = append(s.journal, op)

	st := s.states[op.PlayerID]
	st.AppliedSeq = op.Seq
	st.LastOpTime = op.ClientTime
	switch op.Type {
	case OpMove:
		st.Position = op.Position
	case OpPickup:
		st.Resource += op.Amount
	case OpAttack:
		if tgt, ok := s.states[op.TargetID]; ok {
			tgt.Health -= op.Amount
			s.states[op.TargetID] = tgt
		}
	}
	s.states[op.PlayerID] = st
	s.version++
	s.mu.Unlock()

	// 规则评估（lane 本地状态，无需会话锁）。
	verdicts := lane.judge.observe(op, logicalTime)
	if len(verdicts) == 0 {
		s.mu.Lock()
		s.publishLocked()
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range verdicts {
		if s.verdictN >= s.cfg.MaxVerdictsPerSession {
			break // 判定记录上界：超出不再记录，但不影响状态推进
		}
		v.MatchID = s.matchID
		v.PlayerID = op.PlayerID
		s.store.Append(v)
		s.verdictN++
	}
	s.publishLocked()
}

// publishLocked 用当前权威状态构建并发布新的不可变快照。调用时需持有 s.mu。
func (s *Session) publishLocked() {
	players := make(map[string]PlayerState, len(s.states))
	for k, v := range s.states {
		players[k] = v
	}
	s.snapshot = Snapshot{
		MatchID:     s.matchID,
		Version:     s.version,
		RuleVersion: s.rules.Version,
		Players:     players,
	}
}

// Snapshot 返回当前已提交的权威状态快照（不可变，自洽的单个版本）。
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

// Close 结束对局，之后所有提交都会被拒绝。
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Journal 返回已应用操作的日志副本，用于重放。
func (s *Session) Journal() []Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Operation, len(s.journal))
	copy(out, s.journal)
	return out
}

// RuleVersion 返回本对局绑定的规则版本。
func (s *Session) RuleVersion() string {
	return s.rules.Version
}
