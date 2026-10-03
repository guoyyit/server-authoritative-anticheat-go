// Package anticheat 提供进程内的服务端权威对局行为校验与作弊判定能力。
package anticheat

// OpType 是客户端上报操作的类型。
type OpType string

const (
	OpMove    OpType = "move"    // 位移：携带客户端声称的落点
	OpAttack  OpType = "attack"  // 攻击：携带目标与伤害量
	OpPickup  OpType = "pickup"  // 资源拾取：携带数量
	OpUseItem OpType = "useitem" // 使用道具
)

// Vec3 是三维坐标。
type Vec3 struct {
	X, Y, Z float64
}

// Operation 是客户端上报的一次操作。Seq 为每玩家单调递增序号，
// ClientTime 为客户端本地毫秒时间戳，Checksum 为防篡改校验值。
type Operation struct {
	MatchID    string
	PlayerID   string
	Seq        uint64
	ClientTime int64
	Type       OpType
	Position   Vec3
	TargetID   string
	Amount     float64
	Checksum   uint64
}

// RejectReason 区分各类输入拒绝原因，彼此可区分。
type RejectReason string

const (
	RejectNone          RejectReason = ""
	RejectSeqRollback   RejectReason = "seq_rollback"   // 序号回退
	RejectSeqGap        RejectReason = "seq_gap"        // 序号跳跃过大
	RejectDuplicate     RejectReason = "duplicate"      // 重复提交
	RejectForgedTime    RejectReason = "forged_time"    // 伪造时间
	RejectTampered      RejectReason = "tampered"       // 校验值不符，疑似篡改
	RejectQueueFull     RejectReason = "queue_full"     // 在途队列已满（背压）
	RejectSessionClosed RejectReason = "session_closed" // 对局已结束
	RejectUnknownMatch  RejectReason = "unknown_match"  // 对局不存在
	RejectUnknownPlayer RejectReason = "unknown_player" // 玩家不在对局中
	RejectBufferFull    RejectReason = "reorder_buffer" // 乱序缓冲已满
)

// Receipt 是每次提交的显式回执：任何输入都有结论，不会被静默丢弃。
type Receipt struct {
	Accepted bool
	Reason   RejectReason
	Detail   string
}

// Severity 是判定结论的严重程度分级。
type Severity int

const (
	SeverityNone       Severity = iota // 未违规
	SeverityBorderline                 // 边界情形：落在容差带边缘
	SeverityMinor                      // 轻微超限
	SeverityMajor                      // 显著超限
	SeverityCritical                   // 严重超限
)

func (s Severity) String() string {
	switch s {
	case SeverityNone:
		return "none"
	case SeverityBorderline:
		return "borderline"
	case SeverityMinor:
		return "minor"
	case SeverityMajor:
		return "major"
	case SeverityCritical:
		return "critical"
	}
	return "unknown"
}

// RuleID 标识一条规则。
type RuleID string

const (
	RuleSpeed        RuleID = "speed"        // 移动速度
	RuleDisplacement RuleID = "displacement" // 单次位移距离
	RuleAttackRate   RuleID = "attack_rate"  // 攻击频率
	RuleDamage       RuleID = "damage"       // 单次伤害
	RuleResource     RuleID = "resource"     // 资源获取速率
)

// Verdict 是一条判定结论，可追溯到具体操作与规则。
// 判定只依赖操作数据与逻辑时钟，保证同版本规则下重放逐条一致。
type Verdict struct {
	ID          uint64
	MatchID     string
	PlayerID    string
	RuleID      RuleID
	RuleVersion string
	OpSeq       uint64  // 触发判定的操作序号
	Observed    float64 // 观测值
	AllowedMin  float64 // 允许下限
	AllowedMax  float64 // 允许上限（含容差）
	Severity    Severity
	Detail      string
	LogicalTime uint64 // 逻辑时钟：已应用操作计数，保证确定性
}

// Review 是对某条判定的复核记录。复核追加新记录，不改写原判定。
type Review struct {
	VerdictID   uint64
	ReviewerID  string
	Outcome     ReviewOutcome
	Reason      string
	NewSeverity Severity // 仅当 Outcome 为调整等级时有效
	LogicalTime uint64
}

// ReviewOutcome 是复核结论。
type ReviewOutcome string

const (
	ReviewUpheld     ReviewOutcome = "upheld"     // 维持原判
	ReviewOverturned ReviewOutcome = "overturned" // 推翻（视为误报）
	ReviewAdjusted   ReviewOutcome = "adjusted"   // 调整严重等级
)

// PlayerState 是服务端权威的玩家状态（不可变快照的一部分）。
type PlayerState struct {
	PlayerID   string
	Position   Vec3
	Health     float64
	Resource   float64
	AppliedSeq uint64 // 已连续应用到的序号（水位线）
	LastOpTime int64  // 最近一次应用的客户端时间
}

// Snapshot 是某一已提交版本的对局状态，发布后不可变。
type Snapshot struct {
	MatchID     string
	Version     uint64 // 单调递增的提交版本号
	RuleVersion string
	Players     map[string]PlayerState
}
