package anticheat

import "sync"

// RuleSet 是一个不可变的规则集版本。对局在开始时绑定某一版本，
// 之后规则升级不影响已绑定对局。
type RuleSet struct {
	Version string

	// 位移/速度
	MaxSpeed        float64 // 允许的最大移动速度（单位/秒）
	MaxStepDistance float64 // 单次操作允许的最大位移

	// 攻击
	MinAttackIntervalMs int64   // 两次攻击的最小间隔
	MaxDamagePerHit     float64 // 单次攻击伤害上限

	// 资源
	MaxResourcePerSecond float64 // 资源获取速率上限

	// 容差（比例）：观测值 <= 上限*(1+Tolerance) 视为正常网络抖动
	Tolerance float64
	// 边界带（比例）：超过容差但在上限*(1+BorderlineBand) 内归为边界情形
	BorderlineBand float64
	// 严重等级分档（相对上限的超限比例）
	MajorBand    float64
	CriticalBand float64

	// 持续违规窗口：连续 ViolationWindow 次超限才产出正式判定，
	// 单次延迟尖峰不会误判。
	ViolationWindow int

	// 输入校验
	MaxSeqGap       uint64 // 允许的最大序号跳跃
	MaxClockSkewMs  int64  // 客户端时间允许的最大回退（毫秒）
	MinOpIntervalMs int64  // 相邻操作客户端时间的最小间隔（防伪造成批操作）
}

// DefaultRuleSet 返回一份合理的默认规则（版本 "v1"）。
func DefaultRuleSet() *RuleSet {
	return &RuleSet{
		Version:              "v1",
		MaxSpeed:             10,
		MaxStepDistance:      5,
		MinAttackIntervalMs:  200,
		MaxDamagePerHit:      100,
		MaxResourcePerSecond: 50,
		Tolerance:            0.15,
		BorderlineBand:       0.35,
		MajorBand:            1.0,
		CriticalBand:         3.0,
		ViolationWindow:      3,
		MaxSeqGap:            64,
		MaxClockSkewMs:       500,
		MinOpIntervalMs:      0,
	}
}

// RuleRegistry 管理规则集的多版本演进。注册后版本不可变。
type RuleRegistry struct {
	mu      sync.RWMutex
	byVer   map[string]*RuleSet
	current string
}

func NewRuleRegistry() *RuleRegistry {
	return &RuleRegistry{byVer: make(map[string]*RuleSet)}
}

// Register 注册一个规则版本；同名重复注册会被忽略（保持不可变语义）。
func (r *RuleRegistry) Register(rs *RuleSet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byVer[rs.Version]; ok {
		return
	}
	r.byVer[rs.Version] = rs
	r.current = rs.Version
}

// Current 返回当前生效的规则版本。
func (r *RuleRegistry) Current() *RuleSet {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byVer[r.current]
}

// Get 按版本号查询历史规则。
func (r *RuleRegistry) Get(version string) (*RuleSet, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rs, ok := r.byVer[version]
	return rs, ok
}
