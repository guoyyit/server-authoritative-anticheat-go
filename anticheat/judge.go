package anticheat

import (
	"fmt"
	"math"
)

// judge 是单玩家的规则评估器：基于已按序应用的操作流与规则集做一致性判定。
//
// 判定语义：
//   - 每条规则把观测值与允许上限比较，超限比例 r = observed/limit - 1；
//   - r <= Tolerance 视为正常网络抖动，不计违规；
//   - 单次超限只累计"连续违规计数"，达到 ViolationWindow 才产出正式判定，
//     因此一次延迟尖峰不会误判；
//   - 严重程度按当前连续违规段内的最大超限比例分档。
type judge struct {
	rules *RuleSet

	lastPos  Vec3
	lastTime int64
	hasLast  bool

	lastAttackAt int64
	hasAttack    bool

	strikes  map[RuleID]int     // 每条规则的连续违规计数
	maxRatio map[RuleID]float64 // 当前违规段内的最大超限比例

	// 资源速率窗口（按客户端时间滑动）
	resWindowStart int64
	resInWindow    float64
}

func newJudge(rules *RuleSet) *judge {
	return &judge{
		rules:    rules,
		strikes:  make(map[RuleID]int),
		maxRatio: make(map[RuleID]float64),
	}
}

// severityFor 按超限比例分档。
func (j *judge) severityFor(ratio float64) Severity {
	switch {
	case ratio <= j.rules.Tolerance:
		return SeverityNone
	case ratio <= j.rules.BorderlineBand:
		return SeverityBorderline
	case ratio <= j.rules.MajorBand:
		return SeverityMinor
	case ratio <= j.rules.CriticalBand:
		return SeverityMajor
	default:
		return SeverityCritical
	}
}

// check 累计一条规则的观测结果，必要时产出判定。
// observed 为观测值，limit 为原始上限，ratio 为超限比例（observed/limit - 1）。
func (j *judge) check(rule RuleID, op Operation, observed, limit, ratio float64, unit string, logicalTime uint64) *Verdict {
	if ratio <= j.rules.Tolerance {
		j.strikes[rule] = 0
		j.maxRatio[rule] = 0
		return nil
	}
	j.strikes[rule]++
	if ratio > j.maxRatio[rule] {
		j.maxRatio[rule] = ratio
	}
	if j.strikes[rule] < j.rules.ViolationWindow {
		return nil
	}
	worst := j.maxRatio[rule]
	sev := j.severityFor(worst)
	allowed := limit * (1 + j.rules.Tolerance)
	v := &Verdict{
		RuleID:      rule,
		RuleVersion: j.rules.Version,
		OpSeq:       op.Seq,
		Observed:    observed,
		AllowedMin:  0,
		AllowedMax:  allowed,
		Severity:    sev,
		Detail: fmt.Sprintf("%s: observed %.4g %s exceeds limit %.4g (tolerance %.0f%%, sustained %d ops, worst excess %.0f%%)",
			rule, observed, unit, limit, j.rules.Tolerance*100, j.strikes[rule], worst*100),
		LogicalTime: logicalTime,
	}
	j.strikes[rule] = 0
	j.maxRatio[rule] = 0
	return v
}

// observe 评估一条已按序应用的操作，返回产生的判定（可能为空）。
// 只依赖操作字段与逻辑时钟，保证重放确定性。
func (j *judge) observe(op Operation, logicalTime uint64) []Verdict {
	var out []Verdict
	emit := func(v *Verdict) {
		if v != nil {
			out = append(out, *v)
		}
	}

	switch op.Type {
	case OpMove:
		if j.hasLast {
			dist := distance(j.lastPos, op.Position)
			// 单次位移
			ratio := dist/j.rules.MaxStepDistance - 1
			emit(j.check(RuleDisplacement, op, dist, j.rules.MaxStepDistance, ratio, "units", logicalTime))
			// 速度：仅在客户端时间前进时评估
			dtMs := op.ClientTime - j.lastTime
			if dtMs > 0 {
				speed := dist / (float64(dtMs) / 1000)
				ratio := speed/j.rules.MaxSpeed - 1
				emit(j.check(RuleSpeed, op, speed, j.rules.MaxSpeed, ratio, "units/s", logicalTime))
			}
		}
		j.lastPos = op.Position
		j.lastTime = op.ClientTime
		j.hasLast = true

	case OpAttack:
		// 单次伤害
		ratio := op.Amount/j.rules.MaxDamagePerHit - 1
		emit(j.check(RuleDamage, op, op.Amount, j.rules.MaxDamagePerHit, ratio, "dmg", logicalTime))
		// 攻击频率：间隔过短为违规，超限比例按 limit/observed - 1 计算
		if j.hasAttack {
			interval := op.ClientTime - j.lastAttackAt
			if interval >= 0 {
				limit := float64(j.rules.MinAttackIntervalMs)
				obs := float64(interval)
				ratio := limit/maxf(obs, 1) - 1
				if v := j.check(RuleAttackRate, op, obs, limit, ratio, "ms interval", logicalTime); v != nil {
					// 间隔类规则：观测值越小越违规，允许范围是下限
					v.AllowedMin = limit * (1 - j.rules.Tolerance)
					v.AllowedMax = math.Inf(1)
					out = append(out, *v)
				}
			}
		}
		j.lastAttackAt = op.ClientTime
		j.hasAttack = true

	case OpPickup:
		if j.resWindowStart == 0 {
			j.resWindowStart = op.ClientTime
		}
		j.resInWindow += op.Amount
		elapsed := op.ClientTime - j.resWindowStart
		if elapsed >= 1000 {
			rate := j.resInWindow * 1000 / float64(elapsed)
			ratio := rate/j.rules.MaxResourcePerSecond - 1
			emit(j.check(RuleResource, op, rate, j.rules.MaxResourcePerSecond, ratio, "units/s", logicalTime))
			j.resWindowStart = op.ClientTime
			j.resInWindow = 0
		}
	}
	return out
}

func distance(a, b Vec3) float64 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
