package anticheat

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
)

// deriveKey 由对局与玩家派生会话级防篡改密钥。
// 密钥在加入对局时下发给客户端，不上报在网络中。
func deriveKey(matchID, playerID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(matchID))
	h.Write([]byte{0})
	h.Write([]byte(playerID))
	return h.Sum64()
}

// computeChecksum 计算操作的防篡改校验值（FNV-1a 覆盖全部权威字段 + 密钥）。
// 客户端与服务端用同一算法；任何字段被篡改都会导致校验失败。
func computeChecksum(op Operation, key uint64) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	putU64 := func(v uint64) { binary.LittleEndian.PutUint64(buf[:], v); h.Write(buf[:]) }
	putI64 := func(v int64) { putU64(uint64(v)) }
	putF64 := func(v float64) { putU64(math.Float64bits(v)) }

	h.Write([]byte(op.MatchID))
	h.Write([]byte{0})
	h.Write([]byte(op.PlayerID))
	h.Write([]byte{0})
	h.Write([]byte(op.Type))
	h.Write([]byte{0})
	h.Write([]byte(op.TargetID))
	h.Write([]byte{0})
	putU64(op.Seq)
	putI64(op.ClientTime)
	putF64(op.Position.X)
	putF64(op.Position.Y)
	putF64(op.Position.Z)
	putF64(op.Amount)
	putU64(key)
	return h.Sum64()
}

// Sign 为合法客户端提供便利：计算并填充校验值。
func Sign(op Operation, matchID, playerID string) Operation {
	op.Checksum = computeChecksum(op, deriveKey(matchID, playerID))
	return op
}

// validateInput 对单条操作做与顺序无关的静态校验：
// 防篡改校验值、伪造时间。lastClientTime 为该玩家上一条已接收操作的客户端时间。
// 校验失败必须返回可区分的原因，绝不静默丢弃。
func validateInput(op Operation, key uint64, rules *RuleSet, lastClientTime int64) Receipt {
	if op.Checksum != computeChecksum(op, key) {
		return Receipt{Accepted: false, Reason: RejectTampered,
			Detail: "checksum mismatch: operation fields were modified in transit"}
	}
	// 伪造时间：客户端时间相对自己此前的上报出现明显回退。
	// 允许 MaxClockSkewMs 以内的乱序抖动（乱序到达本身由排序器处理）。
	if lastClientTime != 0 && op.ClientTime < lastClientTime-rules.MaxClockSkewMs {
		return Receipt{Accepted: false, Reason: RejectForgedTime,
			Detail: fmt.Sprintf("client time %d rewinds %d ms beyond skew allowance",
				op.ClientTime, lastClientTime-op.ClientTime)}
	}
	return Receipt{Accepted: true}
}
