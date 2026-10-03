package anticheat

import (
	"fmt"
	"sync"
	"testing"
)

// 构造一条签名正确的操作。
func mkOp(match, player string, seq uint64, t int64, typ OpType, pos Vec3, amount float64) Operation {
	return Sign(Operation{
		MatchID: match, PlayerID: player, Seq: seq, ClientTime: t,
		Type: typ, Position: pos, Amount: amount,
	}, match, player)
}

func mkMove(match, player string, seq uint64, t int64, x float64) Operation {
	return mkOp(match, player, seq, t, OpMove, Vec3{X: x}, 0)
}

func newEngineWith(t *testing.T, rules *RuleSet, cfg Config) *Engine {
	t.Helper()
	reg := NewRuleRegistry()
	reg.Register(rules)
	return NewEngine(cfg, reg, NewVerdictStore())
}

func mustStart(t *testing.T, e *Engine, match string, players ...string) *Session {
	t.Helper()
	s, r := e.StartMatch(match, players)
	if !r.Accepted {
		t.Fatalf("start match: %s", r.Detail)
	}
	return s
}

func mustAccept(t *testing.T, s *Session, op Operation) {
	t.Helper()
	if r := s.Submit(op); !r.Accepted {
		t.Fatalf("op seq=%d rejected: %s %s", op.Seq, r.Reason, r.Detail)
	}
}

func mustReject(t *testing.T, s *Session, op Operation, want RejectReason) {
	t.Helper()
	r := s.Submit(op)
	if r.Accepted || r.Reason != want {
		t.Fatalf("op seq=%d: got accepted=%v reason=%q, want reject %q", op.Seq, r.Accepted, r.Reason, want)
	}
}

// 乱序到达：应用结果必须与按序号顺序处理一致。
func TestOutOfOrderAppliedInSeqOrder(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	mustAccept(t, s, mkMove("m1", "p1", 1, 1000, 0))
	mustAccept(t, s, mkMove("m1", "p1", 3, 1200, 1.0)) // 乱序先到的 seq 3
	mustAccept(t, s, mkMove("m1", "p1", 2, 1100, 0.5)) // seq 2 到达后 2、3 依次应用
	mustAccept(t, s, mkMove("m1", "p1", 4, 1300, 1.5))

	snap := s.Snapshot()
	st := snap.Players["p1"]
	if st.AppliedSeq != 4 {
		t.Fatalf("applied seq = %d, want 4", st.AppliedSeq)
	}
	if st.Position.X != 1.5 {
		t.Fatalf("position = %v, want 1.5（按序号顺序应用的最终位置）", st.Position.X)
	}
	if st.LastOpTime != 1300 {
		t.Fatalf("last op time = %d, want 1300", st.LastOpTime)
	}
}

// 重复提交：不得被应用两次。
func TestDuplicateNotAppliedTwice(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	op := mkOp("m1", "p1", 1, 1000, OpPickup, Vec3{}, 10)
	mustAccept(t, s, op)
	mustReject(t, s, op, RejectDuplicate) // 完全相同的重发

	// 缓冲中的重复也算重复。
	mustAccept(t, s, mkMove("m1", "p1", 3, 3000, 2))
	mustReject(t, s, mkMove("m1", "p1", 3, 3000, 2), RejectDuplicate)

	if got := s.Snapshot().Players["p1"].Resource; got != 10 {
		t.Fatalf("resource = %v, want 10（拾取只应用一次）", got)
	}
}

// 迟到/回退操作：不得改写已发布的权威状态。
func TestLateOpDoesNotRewritePublishedState(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	mustAccept(t, s, mkMove("m1", "p1", 1, 1000, 0))
	mustAccept(t, s, mkMove("m1", "p1", 2, 2000, 3))
	before := s.Snapshot()

	// 同序号、不同内容：序号回退，拒绝且不改写状态。
	mustReject(t, s, mkMove("m1", "p1", 1, 1500, 99), RejectSeqRollback)

	after := s.Snapshot()
	if after.Players["p1"].Position.X != 3 || after.Version != before.Version {
		t.Fatalf("published state rewritten by late op: %+v", after.Players["p1"])
	}
}

// 异常输入：序号跳跃、伪造时间、篡改字段，各自有可区分的原因。
func TestAnomalousInputsRejected(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	// 序号跳跃
	mustReject(t, s, mkMove("m1", "p1", 1000, 1000, 0), RejectSeqGap)

	// 伪造时间：客户端时间大幅回退
	mustAccept(t, s, mkMove("m1", "p1", 1, 10000, 0))
	mustReject(t, s, mkMove("m1", "p1", 2, 1000, 1), RejectForgedTime)

	// 篡改：签名后修改字段
	op := mkOp("m1", "p1", 2, 10500, OpPickup, Vec3{}, 10)
	op.Amount = 99999
	mustReject(t, s, op, RejectTampered)

	// 未知玩家 / 未知对局
	mustReject(t, s, mkMove("m1", "ghost", 1, 1000, 0), RejectUnknownPlayer)
	if r := e.Submit(mkMove("nope", "p1", 1, 1000, 0)); r.Reason != RejectUnknownMatch {
		t.Fatalf("unknown match: got %q", r.Reason)
	}

	// 异常输入不影响后续正常处理
	mustAccept(t, s, mkMove("m1", "p1", 2, 11000, 1))
	if got := s.Snapshot().Players["p1"].AppliedSeq; got != 2 {
		t.Fatalf("applied seq = %d, want 2", got)
	}
}

// 对局关闭后拒绝提交。
func TestClosedSessionRejects(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")
	s.Close()
	mustReject(t, s, mkMove("m1", "p1", 1, 1000, 0), RejectSessionClosed)
}

// 持续超速被判出；结论包含观测值与允许范围。
func TestSpeedCheatDetected(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	// 每 150ms 移动 2 单位 => 13.3 u/s > 10*(1+15%)，持续违规
	for i := uint64(1); i <= 5; i++ {
		mustAccept(t, s, mkMove("m1", "p1", i, int64(i)*150, float64(i)*2))
	}
	vs := e.Store().Verdicts("m1")
	if len(vs) == 0 {
		t.Fatal("expected speed verdict")
	}
	v := vs[0]
	if v.RuleID != RuleSpeed {
		t.Fatalf("rule = %s, want speed", v.RuleID)
	}
	if v.Observed <= v.AllowedMax {
		t.Fatalf("observed %v should exceed allowed %v", v.Observed, v.AllowedMax)
	}
	if v.RuleVersion != "v1" || v.OpSeq == 0 {
		t.Fatalf("verdict not traceable: %+v", v)
	}
}

// 正常网络抖动：单次延迟尖峰不得误判。
func TestJitterSpikeNotMisjudged(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	// 正常节奏：每 400ms 移动 3 单位 => 7.5 u/s
	tm := int64(1000)
	x := 0.0
	seq := uint64(1)
	for i := 0; i < 3; i++ {
		mustAccept(t, s, mkMove("m1", "p1", seq, tm, x))
		seq++
		tm += 400
		x += 3
	}
	// 一次延迟尖峰：上一条延迟堆积，本条间隔仅 50ms => 瞬时 60 u/s
	mustAccept(t, s, mkMove("m1", "p1", seq, tm-350, x))
	seq++
	// 之后恢复正常
	for i := 0; i < 3; i++ {
		tm += 400
		x += 3
		mustAccept(t, s, mkMove("m1", "p1", seq, tm, x))
		seq++
	}
	if vs := e.Store().Verdicts("m1"); len(vs) != 0 {
		t.Fatalf("jitter spike misjudged: %+v", vs)
	}
}

// 容差内的轻微超限不算违规。
func TestWithinToleranceNotFlagged(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	// 10.5 u/s < 10*1.15，容差内；步长 4.2 在单次位移上限内
	for i := uint64(1); i <= 6; i++ {
		mustAccept(t, s, mkMove("m1", "p1", i, int64(i)*400, float64(i)*4.2))
	}
	if vs := e.Store().Verdicts("m1"); len(vs) != 0 {
		t.Fatalf("within-tolerance movement flagged: %+v", vs)
	}
}

// 攻击频率与伤害判定。
func TestAttackRateAndDamage(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1", "p2")

	// 合法伤害、过快攻击：间隔 50ms < 200ms
	for i := uint64(1); i <= 5; i++ {
		mustAccept(t, s, mkOp("m1", "p1", i, int64(i)*50, OpAttack, Vec3{}, 50))
	}
	var rateFound bool
	for _, v := range e.Store().Verdicts("m1") {
		if v.RuleID == RuleAttackRate {
			rateFound = true
			if v.AllowedMin <= 0 {
				t.Fatalf("attack rate verdict should carry lower bound: %+v", v)
			}
		}
	}
	if !rateFound {
		t.Fatal("expected attack_rate verdict")
	}

	// 超高伤害
	s2 := mustStart(t, e, "m2", "q1")
	for i := uint64(1); i <= 4; i++ {
		mustAccept(t, s2, mkOp("m2", "q1", i, int64(i)*300, OpAttack, Vec3{}, 500))
	}
	var dmg *Verdict
	for _, v := range e.Store().Verdicts("m2") {
		if v.RuleID == RuleDamage {
			vv := v
			dmg = &vv
		}
	}
	if dmg == nil {
		t.Fatal("expected damage verdict")
	}
	if dmg.Severity != SeverityMajor { // 500/100-1 = 4 > CriticalBand(3) => critical
		if dmg.Severity != SeverityCritical {
			t.Fatalf("severity = %s", dmg.Severity)
		}
	}
}

// 资源获取速率判定。
func TestResourceRateDetected(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	// 每秒拾取 200，上限 50/s，持续 3 个窗口
	seq := uint64(1)
	for w := 0; w < 4; w++ {
		for i := 0; i < 4; i++ {
			mustAccept(t, s, mkOp("m1", "p1", seq, int64(w*1000+i*250+1000), OpPickup, Vec3{}, 50))
			seq++
		}
	}
	var found bool
	for _, v := range e.Store().Verdicts("m1") {
		if v.RuleID == RuleResource {
			found = true
		}
	}
	if !found {
		t.Fatal("expected resource verdict")
	}
}

// 严重程度分级：边界情形被清楚归类。
func TestSeverityGrading(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())

	// 边界：速度超限 25%（在 borderline 带内）
	s := mustStart(t, e, "m-border", "p1")
	for i := uint64(1); i <= 5; i++ {
		mustAccept(t, s, mkMove("m-border", "p1", i, int64(i)*160, float64(i)*2)) // 12.5 u/s
	}
	vs := e.Store().Verdicts("m-border")
	if len(vs) == 0 || vs[0].Severity != SeverityBorderline {
		t.Fatalf("want borderline verdict, got %+v", vs)
	}

	// 严重：速度超限 400%
	s2 := mustStart(t, e, "m-crit", "p2")
	for i := uint64(1); i <= 5; i++ {
		mustAccept(t, s2, mkMove("m-crit", "p2", i, int64(i)*100, float64(i)*5)) // 50 u/s
	}
	vs2 := e.Store().Verdicts("m-crit")
	var maxSev Severity
	for _, v := range vs2 {
		if v.RuleID == RuleSpeed && v.Severity > maxSev {
			maxSev = v.Severity
		}
	}
	if maxSev != SeverityCritical {
		t.Fatalf("want critical speed verdict, got %s", maxSev)
	}
}

// 重放一致性：同一对局同一规则版本重放，结论逐条一致。
func TestReplayMatchesOnline(t *testing.T) {
	rules := DefaultRuleSet()
	cfg := DefaultConfig()
	e := newEngineWith(t, rules, cfg)
	s := mustStart(t, e, "m1", "p1", "p2")

	// 混合：乱序、正常移动、超速作弊、攻击
	ops := []Operation{
		mkMove("m1", "p1", 1, 1000, 0),
		mkMove("m1", "p1", 3, 1150, 4), // 乱序
		mkMove("m1", "p1", 2, 1100, 2), // 补齐
		mkMove("m1", "p1", 4, 1300, 6), // 超速段开始
		mkMove("m1", "p1", 5, 1450, 8),
		mkMove("m1", "p1", 6, 1600, 10),
		mkOp("m1", "p2", 1, 1000, OpAttack, Vec3{}, 50),
		mkOp("m1", "p2", 2, 1050, OpAttack, Vec3{}, 50), // 攻击过快
		mkOp("m1", "p2", 3, 1100, OpAttack, Vec3{}, 50),
		mkOp("m1", "p2", 4, 1150, OpAttack, Vec3{}, 50),
	}
	for _, op := range ops {
		s.Submit(op)
	}
	online := e.Store().Verdicts("m1")
	if len(online) == 0 {
		t.Fatal("expected online verdicts")
	}

	replayed := Replay("m1", s.Journal(), rules, cfg)
	if len(replayed) != len(online) {
		t.Fatalf("replay verdict count %d != online %d", len(replayed), len(online))
	}
	for i := range online {
		a, b := online[i], replayed[i]
		a.ID, b.ID = 0, 0 // ID 由各自存储分配，不参与比较
		if a != b {
			t.Fatalf("verdict %d differs:\nonline:  %+v\nreplayed: %+v", i, a, b)
		}
	}
}

// 规则版本切换：进行中的对局绑定旧版本，新对局用新版本，历史可按版本追溯。
func TestRuleVersionSwitch(t *testing.T) {
	reg := NewRuleRegistry()
	v1 := DefaultRuleSet() // 伤害上限 100
	reg.Register(v1)
	e := NewEngine(DefaultConfig(), reg, NewVerdictStore())

	s1 := mustStart(t, e, "old", "p1")

	// 升级规则：伤害上限降到 50
	v2 := *v1
	v2.Version = "v2"
	v2.MaxDamagePerHit = 50
	reg.Register(&v2)

	s2 := mustStart(t, e, "new", "p2")

	// 旧对局：伤害 80 在 v1 下合法，不应被判
	for i := uint64(1); i <= 4; i++ {
		mustAccept(t, s1, mkOp("old", "p1", i, int64(i)*300, OpAttack, Vec3{}, 80))
	}
	if vs := e.Store().Verdicts("old"); len(vs) != 0 {
		t.Fatalf("old match judged by new rules: %+v", vs)
	}

	// 新对局：伤害 80 在 v2 下超限，应被判且标记 v2
	for i := uint64(1); i <= 4; i++ {
		mustAccept(t, s2, mkOp("new", "p2", i, int64(i)*300, OpAttack, Vec3{}, 80))
	}
	vs := e.Store().Verdicts("new")
	if len(vs) == 0 {
		t.Fatal("expected damage verdict under v2")
	}
	for _, v := range vs {
		if v.RuleVersion != "v2" {
			t.Fatalf("verdict bound to %s, want v2", v.RuleVersion)
		}
	}

	// 按版本追溯：v1 无判定，v2 有判定
	if got := len(e.Store().VerdictsByRuleVersion("v1")); got != 0 {
		t.Fatalf("v1 verdicts = %d, want 0", got)
	}
	if got := len(e.Store().VerdictsByRuleVersion("v2")); got == 0 {
		t.Fatal("v2 verdicts missing")
	}

	// 旧对局重放仍用 v1，结论一致（无判定）
	if got := Replay("old", s1.Journal(), v1, DefaultConfig()); len(got) != 0 {
		t.Fatalf("replay of old match under v1 produced verdicts: %+v", got)
	}
}

// 并发校验：多玩家并发提交互不阻塞，状态始终自洽。
func TestConcurrentPlayers(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	players := []string{"p1", "p2", "p3", "p4"}
	s := mustStart(t, e, "m1", players...)

	const opsPerPlayer = 100
	var wg sync.WaitGroup
	for _, p := range players {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := uint64(1); i <= opsPerPlayer; i++ {
				// 正常节奏移动
				if r := s.Submit(mkMove("m1", p, i, int64(i)*400, float64(i)*3)); !r.Accepted {
					t.Errorf("%s seq %d rejected: %s", p, i, r.Reason)
				}
			}
		}()
	}
	// 并发读取快照：任何时刻都必须是自洽的已提交版本
	stop := make(chan struct{})
	var readerWg sync.WaitGroup
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := s.Snapshot()
			var sum uint64
			for _, st := range snap.Players {
				sum += st.AppliedSeq
			}
			if sum != snap.Version {
				t.Errorf("inconsistent snapshot: version %d, applied sum %d", snap.Version, sum)
			}
		}
	}()
	wg.Wait()
	close(stop)
	readerWg.Wait()

	snap := s.Snapshot()
	for _, p := range players {
		if got := snap.Players[p].AppliedSeq; got != opsPerPlayer {
			t.Fatalf("%s applied %d, want %d", p, got, opsPerPlayer)
		}
	}
	if snap.Version != uint64(len(players)*opsPerPlayer) {
		t.Fatalf("version = %d", snap.Version)
	}
}

// 恶意高频上报：在途上界触发背压拒绝，且不影响其他玩家。
func TestFloodingPlayerThrottled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InboxCapacity = 1
	e := newEngineWith(t, DefaultRuleSet(), cfg)
	s := mustStart(t, e, "m1", "flood", "normal")

	// 先正常应用 seq 1
	mustAccept(t, s, mkMove("m1", "flood", 1, 1000, 0))

	// 白盒：占满 flood 的在途名额，验证背压拒绝
	lane := s.lanes["flood"]
	lane.sem <- struct{}{}
	r := s.Submit(mkMove("m1", "flood", 2, 2000, 1))
	if r.Reason != RejectQueueFull {
		t.Fatalf("got %q, want queue_full", r.Reason)
	}
	<-lane.sem

	// 其他玩家不受影响
	mustAccept(t, s, mkMove("m1", "normal", 1, 1000, 0))

	// 洪水式重复提交全部被显式拒绝，不崩溃、不静默
	rejected := map[RejectReason]int{}
	for i := 0; i < 1000; i++ {
		r := s.Submit(mkMove("m1", "flood", 1, 1000, 0))
		if r.Accepted {
			t.Fatal("duplicate flood op accepted")
		}
		rejected[r.Reason]++
	}
	if rejected[RejectDuplicate] != 1000 {
		t.Fatalf("flood rejects: %v", rejected)
	}
}

// 判定记录上界。
func TestVerdictCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxVerdictsPerSession = 2
	rules := DefaultRuleSet()
	rules.ViolationWindow = 1 // 每次违规立即产出判定
	e := newEngineWith(t, rules, cfg)
	s := mustStart(t, e, "m1", "p1")

	for i := uint64(1); i <= 10; i++ {
		mustAccept(t, s, mkOp("m1", "p1", i, int64(i)*300, OpAttack, Vec3{}, 500))
	}
	if got := len(e.Store().Verdicts("m1")); got != 2 {
		t.Fatalf("verdicts = %d, want cap 2", got)
	}
	// 状态推进不受判定上限影响
	if got := s.Snapshot().Players["p1"].AppliedSeq; got != 10 {
		t.Fatalf("applied = %d, want 10", got)
	}
}

// 复核：不改写原始判定，有效等级随复核变化。
func TestAppealReview(t *testing.T) {
	rules := DefaultRuleSet()
	e := newEngineWith(t, rules, DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	for i := uint64(1); i <= 5; i++ {
		mustAccept(t, s, mkMove("m1", "p1", i, int64(i)*150, float64(i)*2))
	}
	vs := e.Store().Verdicts("m1")
	if len(vs) == 0 {
		t.Fatal("expected verdict")
	}
	v := vs[0]
	orig := v.Severity

	// 复核推翻
	e.Store().AddReview(Review{VerdictID: v.ID, ReviewerID: "admin", Outcome: ReviewOverturned, Reason: "confirmed network jitter"})

	// 原记录不变
	got, ok := e.Store().Verdict(v.ID)
	if !ok || got.Severity != orig {
		t.Fatalf("original verdict mutated: %+v", got)
	}
	// 有效等级已推翻
	eff, _ := e.Store().EffectiveSeverity(v.ID)
	if eff != SeverityNone {
		t.Fatalf("effective severity = %s, want none after overturn", eff)
	}
	// 复核记录可追溯
	if rs := e.Store().Reviews(v.ID); len(rs) != 1 || rs[0].Outcome != ReviewOverturned {
		t.Fatalf("reviews: %+v", rs)
	}
}

// 快照不可变：读到的快照不随后续提交改变。
func TestSnapshotImmutability(t *testing.T) {
	e := newEngineWith(t, DefaultRuleSet(), DefaultConfig())
	s := mustStart(t, e, "m1", "p1")

	mustAccept(t, s, mkMove("m1", "p1", 1, 1000, 5))
	snap1 := s.Snapshot()
	mustAccept(t, s, mkMove("m1", "p1", 2, 2000, 8))

	if snap1.Players["p1"].Position.X != 5 {
		t.Fatalf("old snapshot mutated: %+v", snap1.Players["p1"])
	}
	if s.Snapshot().Players["p1"].Position.X != 8 {
		t.Fatal("new snapshot not published")
	}
}

// 乱序缓冲上界。
func TestReorderBufferBound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReorderBuffer = 2
	e := newEngineWith(t, DefaultRuleSet(), cfg)
	s := mustStart(t, e, "m1", "p1")

	mustAccept(t, s, mkMove("m1", "p1", 2, 1100, 0.5))
	mustAccept(t, s, mkMove("m1", "p1", 3, 1200, 1.0))
	mustReject(t, s, mkMove("m1", "p1", 4, 1300, 1.5), RejectBufferFull)
	// 补齐 seq 1 后缓冲依次放出
	mustAccept(t, s, mkMove("m1", "p1", 1, 1000, 0))
	if got := s.Snapshot().Players["p1"].AppliedSeq; got != 3 {
		t.Fatalf("applied = %d, want 3", got)
	}
}

// 基准：防止判定路径意外退化。
func BenchmarkSubmitMove(b *testing.B) {
	e := NewEngine(DefaultConfig(), nil, nil)
	s, _ := e.StartMatch("m1", []string{"p1"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := uint64(i + 1)
		s.Submit(mkMove("m1", "p1", seq, int64(seq)*400, float64(seq)*3))
	}
}

// 示例：完整链路。
func ExampleEngine() {
	e := NewEngine(DefaultConfig(), nil, nil)
	s, _ := e.StartMatch("match-1", []string{"alice"})
	for i := uint64(1); i <= 3; i++ {
		op := Sign(Operation{
			MatchID: "match-1", PlayerID: "alice", Seq: i,
			ClientTime: int64(i) * 400, Type: OpMove,
			Position: Vec3{X: float64(i) * 3},
		}, "match-1", "alice")
		s.Submit(op)
	}
	snap := s.Snapshot()
	fmt.Printf("version=%d x=%.0f\n", snap.Version, snap.Players["alice"].Position.X)
	// Output: version=3 x=9
}
